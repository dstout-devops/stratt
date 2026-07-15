package vcenter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/view"
	"github.com/vmware/govmomi/vim25/methods"
	"github.com/vmware/govmomi/vim25/mo"
	vim "github.com/vmware/govmomi/vim25/types"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

// Syncer is this Connector's projection capability (§2.2): bulk enumeration
// plus delta ingestion via the PropertyCollector change feed, flowing through
// the Normalizer into the graph with Provenance.
type Syncer struct {
	cfg    Config
	store  *graph.Store
	log    *slog.Logger
	source types.Source

	// moref → projected state, maintained so delta 'leave' updates can
	// tombstone by identity and Relations can bind vm → host.
	vmUUIDByRef   map[string]string
	hostIDByRef   map[string]string
	entityIDByRef map[string]string
}

// NewSyncer prepares a Syncer for one registered Source.
func NewSyncer(cfg Config, store *graph.Store, log *slog.Logger) *Syncer {
	return &Syncer{
		cfg:           cfg,
		store:         store,
		log:           log.With("connector", "vcenter", "source", cfg.SourceName),
		vmUUIDByRef:   map[string]string{},
		hostIDByRef:   map[string]string{},
		entityIDByRef: map[string]string{},
	}
}

// Register records the Source and claims this Syncer's Facet namespaces in
// the ownership registry (§2.1) — registration precedes any write.
func (s *Syncer) Register(ctx context.Context) error {
	src, err := s.store.RegisterSource(ctx, types.Source{
		Kind:     "vcenter",
		Name:     s.cfg.SourceName,
		Endpoint: s.cfg.Endpoint,
	})
	if err != nil {
		return err
	}
	s.source = src
	for _, o := range s.cfg.FacetNamespaces() {
		if err := s.store.RegisterFacetOwner(ctx, o); err != nil {
			return err
		}
	}
	for _, o := range s.cfg.LabelOwners() {
		if err := s.store.RegisterLabelOwner(ctx, o); err != nil {
			return err
		}
	}
	return nil
}

func (s *Syncer) provenance() types.Provenance {
	return types.Provenance{
		WriterKind: types.WriterSyncer,
		WriterRef:  s.cfg.SyncerRef(),
		SourceID:   s.source.ID,
		At:         time.Now().UTC(),
	}
}

// Run performs a full sync, then holds the delta watch open until ctx ends.
// The watch degrades to periodic full syncs if the change feed errors
// (vcsim's WaitForUpdatesEx fidelity is imperfect — ADR-0007).
func (s *Syncer) Run(ctx context.Context) error {
	client, err := connect(ctx, s.cfg)
	if err != nil {
		return err
	}
	defer client.Logout(ctx) //nolint:errcheck
	s.log.Info("connected", "endpoint", about(client.Client))

	if err := s.FullSync(ctx, client); err != nil {
		return err
	}
	for {
		err := s.watch(ctx, client)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.log.Warn("delta watch interrupted; resyncing", "error", err)
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
		if err := s.FullSync(ctx, client); err != nil {
			return err
		}
	}
}

// FullSync bulk-enumerates hosts and VMs and projects them, then tombstones
// everything this Source no longer reports.
func (s *Syncer) FullSync(ctx context.Context, client *govmomi.Client) error {
	started := time.Now()
	m := view.NewManager(client.Client)

	hv, err := m.CreateContainerView(ctx, client.ServiceContent.RootFolder, []string{"HostSystem"}, true)
	if err != nil {
		return fmt.Errorf("vcenter: host view: %w", err)
	}
	defer hv.Destroy(ctx) //nolint:errcheck
	var hosts []mo.HostSystem
	if err := hv.Retrieve(ctx, []string{"HostSystem"}, hostProps, &hosts); err != nil {
		return fmt.Errorf("vcenter: retrieve hosts: %w", err)
	}

	vv, err := m.CreateContainerView(ctx, client.ServiceContent.RootFolder, []string{"VirtualMachine"}, true)
	if err != nil {
		return fmt.Errorf("vcenter: vm view: %w", err)
	}
	defer vv.Destroy(ctx) //nolint:errcheck
	var vms []mo.VirtualMachine
	if err := vv.Retrieve(ctx, []string{"VirtualMachine"}, vmProps, &vms); err != nil {
		return fmt.Errorf("vcenter: retrieve vms: %w", err)
	}

	prov := s.provenance()
	projector := s.store.NormalizerProjector()

	hostSeen := make([]string, 0, len(hosts))
	for _, h := range hosts {
		up, err := normalizeHost(h)
		if err != nil {
			s.log.Warn("skipping host", "error", err)
			continue
		}
		ids, err := projector.UpsertEntities(ctx, prov, []graph.EntityUpsert{up})
		if err != nil {
			return err
		}
		s.hostIDByRef[h.Self.Value] = ids[0]
		hostSeen = append(hostSeen, up.IdentityKeys["vcenter.host.uuid"])
	}

	vmSeen := make([]string, 0, len(vms))
	for _, vm := range vms {
		if err := s.projectVM(ctx, projector, prov, vm); err != nil {
			if errors.Is(err, graph.ErrIdentityConflict) {
				s.log.Error("identity conflict; not merging (§1.2)", "vm", vm.Name, "error", err)
				continue
			}
			return err
		}
		vmSeen = append(vmSeen, vm.Config.Uuid)
	}

	if _, err := projector.TombstoneAbsent(ctx, prov, "vcenter.uuid", vmSeen); err != nil {
		return err
	}
	if _, err := projector.TombstoneAbsent(ctx, prov, "vcenter.host.uuid", hostSeen); err != nil {
		return err
	}
	if err := s.store.SetSyncCursor(ctx, s.source.ID, "", true); err != nil {
		return err
	}
	s.log.Info("full sync complete", "hosts", len(hosts), "vms", len(vms), "took", time.Since(started).String())
	return nil
}

// projectVM normalizes and projects one VM, its identity map entry, and its
// runs-on Relation.
func (s *Syncer) projectVM(ctx context.Context, projector *graph.Projector, prov types.Provenance, vm mo.VirtualMachine) error {
	up, err := normalizeVM(vm)
	if err != nil {
		s.log.Warn("skipping vm", "error", err)
		return nil
	}
	ids, err := projector.UpsertEntities(ctx, prov, []graph.EntityUpsert{up})
	if err != nil {
		return err
	}
	s.vmUUIDByRef[vm.Self.Value] = vm.Config.Uuid
	s.entityIDByRef[vm.Self.Value] = ids[0]

	if vm.Runtime.Host != nil {
		if hostID, ok := s.hostIDByRef[vm.Runtime.Host.Value]; ok {
			if err := projector.UpsertRelation(ctx, prov, "runs-on", ids[0], hostID); err != nil {
				return err
			}
		}
	}
	return nil
}

// watch holds the PropertyCollector change feed open (delta ingestion, §2.2)
// and re-projects VMs as updates arrive. Returns on ctx end or feed error.
func (s *Syncer) watch(ctx context.Context, client *govmomi.Client) error {
	m := view.NewManager(client.Client)
	vv, err := m.CreateContainerView(ctx, client.ServiceContent.RootFolder, []string{"VirtualMachine"}, true)
	if err != nil {
		return fmt.Errorf("vcenter: watch view: %w", err)
	}
	defer vv.Destroy(ctx) //nolint:errcheck

	pc := property.DefaultCollector(client.Client)
	pf, err := pc.CreateFilter(ctx, vim.CreateFilter{
		Spec: vim.PropertyFilterSpec{
			ObjectSet: []vim.ObjectSpec{{
				Obj:  vv.Reference(),
				Skip: vim.NewBool(true),
				SelectSet: []vim.BaseSelectionSpec{
					&vim.TraversalSpec{Type: "ContainerView", Path: "view"},
				},
			}},
			PropSet: []vim.PropertySpec{{Type: "VirtualMachine", PathSet: vmProps}},
		},
	})
	if err != nil {
		return fmt.Errorf("vcenter: create watch filter: %w", err)
	}
	defer pf.Destroy(ctx) //nolint:errcheck

	req := vim.WaitForUpdatesEx{
		This: pc.Reference(),
		Options: &vim.WaitOptions{
			// Long-poll; the loop re-arms with the version cursor.
			MaxWaitSeconds: vim.NewInt32(60),
		},
	}
	if cursor, err := s.store.SyncCursor(ctx, s.source.ID); err == nil && cursor != "" {
		req.Version = cursor
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		res, err := methods.WaitForUpdatesEx(ctx, client.Client, &req)
		if err != nil {
			return fmt.Errorf("vcenter: wait for updates: %w", err)
		}
		set := res.Returnval
		if set == nil { // long-poll timeout, nothing changed
			continue
		}
		req.Version = set.Version

		var changed []vim.ManagedObjectReference
		var left []vim.ManagedObjectReference
		for _, fs := range set.FilterSet {
			for _, ou := range fs.ObjectSet {
				switch ou.Kind {
				case vim.ObjectUpdateKindEnter, vim.ObjectUpdateKindModify:
					changed = append(changed, ou.Obj)
				case vim.ObjectUpdateKindLeave:
					left = append(left, ou.Obj)
				}
			}
		}
		if err := s.applyDelta(ctx, pc, changed, left); err != nil {
			return err
		}
		if err := s.store.SetSyncCursor(ctx, s.source.ID, set.Version, false); err != nil {
			return err
		}
	}
}

// applyDelta re-projects changed VMs and tombstones departed ones.
func (s *Syncer) applyDelta(ctx context.Context, pc *property.Collector, changed, left []vim.ManagedObjectReference) error {
	prov := s.provenance()
	projector := s.store.NormalizerProjector()

	if len(changed) > 0 {
		var vms []mo.VirtualMachine
		if err := pc.Retrieve(ctx, changed, vmProps, &vms); err != nil {
			return fmt.Errorf("vcenter: retrieve delta: %w", err)
		}
		for _, vm := range vms {
			if err := s.projectVM(ctx, projector, prov, vm); err != nil {
				return err
			}
		}
		s.log.Info("delta projected", "changed", len(vms))
	}
	for _, ref := range left {
		uuid, ok := s.vmUUIDByRef[ref.Value]
		if !ok {
			continue
		}
		if _, err := projector.TombstoneByIdentity(ctx, prov, "vcenter.uuid", uuid); err != nil {
			return err
		}
		delete(s.vmUUIDByRef, ref.Value)
		delete(s.entityIDByRef, ref.Value)
		s.log.Info("delta tombstoned", "vm", ref.Value)
	}
	return nil
}
