package vcenter

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/view"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	vimtypes "github.com/vmware/govmomi/vim25/types"
	"google.golang.org/grpc"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// Config locates the vCenter Source. Credentials arrive resolved from the
// plugin's OWN broker at spawn (§2.5); material never crosses the core.
type Config struct {
	PluginID string // the authenticated channel identity the operator grant is keyed on
	Endpoint string
	Username string
	Password string
	Insecure bool // dev/vcsim only
}

// Server implements the sovereign plugin port for a Syncer-class vCenter plugin.
// It advertises the facet namespaces + tombstone schemes it REQUESTS to own; the
// core-side host honors them only where the operator grant allows.
type Server struct {
	pluginv1.UnimplementedPluginServiceServer
	cfg Config
	log *slog.Logger
}

func NewServer(cfg Config, log *slog.Logger) *Server {
	if cfg.PluginID == "" {
		cfg.PluginID = "vcenter"
	}
	return &Server{cfg: cfg, log: log.With("plugin", "vcenter")}
}

func (s *Server) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:        s.cfg.PluginID,
		ProtocolVersion: "v1",
		Class:           pluginv1.PluginClass_PLUGIN_CLASS_SYNCER,
		// Dual-verb (ADR-0060/0113): OBSERVE the estate, INVOKE the provisioning build Actions.
		Verbs: []pluginv1.Verb{pluginv1.Verb_VERB_OBSERVE, pluginv1.Verb_VERB_INVOKE},
		Contracts: []*pluginv1.ContractDecl{
			{SchemaId: "vm.config"},
			{SchemaId: "vm.runtime"},
			{SchemaId: "net.guest"},
			{SchemaId: "net.subnet"}, // vSphere portgroups project as subnets (ADR-0059)
		},
		// Tombstone schemes per kind (ADR-0096 observe-all + full-sync tombstone). Read breadth
		// (ADR-0115) adds region (datacenter) + availability-zone (cluster) — shared kinds, keyed by
		// vSphere moref. Bare Entities, so no new Facet Contracts.
		TombstoneSchemes: []string{
			"vcenter.uuid", "vcenter.host.uuid", "vcenter.network.moref",
			"vcenter.datacenter.moref", "vcenter.cluster.moref",
		},
		// The `provisioning` capability build Actions (ADR-0113). create-vm is NOT idempotent
		// (each call builds a new VM); it supports a side-effect-free dry-run.
		Capabilities: []string{"provisioning"},
		Actions: []*pluginv1.ActionDecl{
			{
				Name:        actionCreateVM,
				Input:       &pluginv1.ContractRef{SchemaId: "actions/vcenter/create-vm.input"},
				Output:      &pluginv1.ContractRef{SchemaId: "actions/vcenter/create-vm.output"},
				Idempotent:  false,
				DryRunnable: true,
			},
			{
				Name:        actionCreatePortgroup,
				Input:       &pluginv1.ContractRef{SchemaId: "actions/vcenter/create-portgroup.input"},
				Output:      &pluginv1.ContractRef{SchemaId: "actions/vcenter/create-portgroup.output"},
				Idempotent:  false,
				DryRunnable: true,
			},
			// Lifecycle Actions (ADR-0114): power/reconfigure/delete on an existing VM by uuid. delete-vm
			// is idempotent (a re-issue over the sync-lag window is a no-op success, D2); the rest are not.
			lifecycleDecl(actionPowerOff, false),
			lifecycleDecl(actionPowerOn, false),
			lifecycleDecl(actionReset, false),
			lifecycleDecl(actionSuspend, false),
			lifecycleDecl(actionShutdownGuest, false),
			lifecycleDecl(actionReconfigure, false),
			lifecycleDecl(actionDeleteVM, true),
			// Snapshot + mobility + portgroup lifecycle (ADR-0114 slice 2). delete-portgroup is
			// idempotent (tombstone-by-absence); the rest are not.
			lifecycleDecl(actionSnapshotCreate, false),
			lifecycleDecl(actionSnapshotRevert, false),
			lifecycleDecl(actionSnapshotRemove, false),
			lifecycleDecl(actionMigrate, false),
			lifecycleDecl(actionClone, false),
			lifecycleDecl(actionReconfigurePG, false),
			lifecycleDecl(actionDeletePortgroup, true),
		},
	}}, nil
}

// lifecycleDecl builds an ActionDecl for a lifecycle Action by the actions/<name>.{input,output}
// convention (ADR-0114). All lifecycle ops are dry-runnable (a side-effect-free plan).
func lifecycleDecl(name string, idempotent bool) *pluginv1.ActionDecl {
	return &pluginv1.ActionDecl{
		Name:        name,
		Input:       &pluginv1.ContractRef{SchemaId: "actions/" + name + ".input"},
		Output:      &pluginv1.ContractRef{SchemaId: "actions/" + name + ".output"},
		Idempotent:  idempotent,
		DryRunnable: true,
	}
}

// Observe performs a full sync: it enumerates hosts + VMs and streams them as
// ObservedEntities with the full_sync_complete boundary so the host can tombstone
// (ADR-0042). Delta/watch (the PropertyCollector change feed) is the follow-up.
func (s *Server) Observe(_ *pluginv1.ObserveRequest, stream grpc.ServerStreamingServer[pluginv1.ObserveResponse]) error {
	ctx := stream.Context()
	client, err := connect(ctx, s.cfg)
	if err != nil {
		return err
	}
	defer client.Logout(ctx) //nolint:errcheck

	entities, err := enumerate(ctx, client.Client)
	if err != nil {
		return err
	}
	s.log.Info("full sync", "entities", len(entities))
	return stream.Send(&pluginv1.ObserveResponse{Entities: entities, FullSyncComplete: true})
}

func connect(ctx context.Context, cfg Config) (*govmomi.Client, error) {
	u, err := soap.ParseURL(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("vcenter: parse endpoint: %w", err)
	}
	u.User = url.UserPassword(cfg.Username, cfg.Password)
	client, err := govmomi.NewClient(ctx, u, cfg.Insecure)
	if err != nil {
		return nil, fmt.Errorf("vcenter: connect %s: %w", u.Host, err)
	}
	return client, nil
}

// retrieve bulk-reads one managed-object type into a typed slice via a container view (the shared
// enumerate primitive). Generic over the mo.* struct so each kind is one call.
func retrieve[T any](ctx context.Context, m *view.Manager, root vimtypes.ManagedObjectReference, typ string, props []string) ([]T, error) {
	v, err := m.CreateContainerView(ctx, root, []string{typ}, true)
	if err != nil {
		return nil, fmt.Errorf("vcenter: %s view: %w", typ, err)
	}
	defer v.Destroy(ctx) //nolint:errcheck
	var out []T
	if err := v.Retrieve(ctx, []string{typ}, props, &out); err != nil {
		return nil, fmt.Errorf("vcenter: retrieve %s: %w", typ, err)
	}
	return out, nil
}

// regionOf walks a moref up its parent chain (cluster → hostFolder → datacenter) to the containing
// Datacenter's moref — the AZ→region edge target (ADR-0115 D5). parentOf must already hold the folder
// chain (folders are retrieved before clusters). Bounded to guard against a malformed cycle.
func regionOf(start string, parentOf map[string]vimtypes.ManagedObjectReference) (string, bool) {
	cur := start
	for i := 0; i < 64; i++ {
		p, ok := parentOf[cur]
		if !ok {
			return "", false
		}
		if p.Type == "Datacenter" {
			return p.Value, true
		}
		cur = p.Value
	}
	return "", false
}

// enumerate bulk-reads the vSphere inventory and normalizes it to ObservedEntities + Relations. Pure
// content-expertise; no graph writes (the plugin holds no DB path). Observe-all (ADR-0096): every object
// the account reports, not just Stratt-created ones. Order matters for the AZ→region walk: datacenters
// and folders populate parentOf before clusters resolve their region.
func enumerate(ctx context.Context, c *vim25.Client) ([]*pluginv1.ObservedEntity, error) {
	m := view.NewManager(c)
	root := c.ServiceContent.RootFolder
	var out []*pluginv1.ObservedEntity
	hostUUIDByRef := map[string]string{}                     // host moref -> vcenter.host.uuid (runs-on target)
	parentOf := map[string]vimtypes.ManagedObjectReference{} // moref -> parent ref (AZ→region walk)

	// Networks (portgroups / DVPGs / opaque) → subnet Entities (ADR-0059/0060).
	networks, err := retrieve[mo.Network](ctx, m, root, "Network", networkProps)
	if err != nil {
		return nil, err
	}
	for _, n := range networks {
		if e, err := normalizeNetwork(n); err == nil {
			out = append(out, e)
		}
	}

	// Datacenters → the SHARED `region` kind (ADR-0115 D1). Seed the parent map.
	datacenters, err := retrieve[mo.Datacenter](ctx, m, root, "Datacenter", datacenterProps)
	if err != nil {
		return nil, err
	}
	for _, d := range datacenters {
		if d.Parent != nil {
			parentOf[d.Self.Value] = *d.Parent
		}
		if e, err := normalizeRegion(d); err == nil {
			out = append(out, e)
		}
	}

	// Folders — retrieved ONLY to complete the parent chain for the AZ→region walk (not projected as
	// Entities until slice 3). Must precede clusters so cluster → hostFolder → datacenter resolves.
	folders, err := retrieve[mo.Folder](ctx, m, root, "Folder", folderProps)
	if err != nil {
		return nil, err
	}
	for _, f := range folders {
		if f.Parent != nil {
			parentOf[f.Self.Value] = *f.Parent
		}
	}

	// Clusters → the SHARED `availability-zone` kind + the in-region edge (ADR-0115 D1/D5).
	clusters, err := retrieve[mo.ClusterComputeResource](ctx, m, root, "ClusterComputeResource", clusterProps)
	if err != nil {
		return nil, err
	}
	for _, cl := range clusters {
		if cl.Parent != nil {
			parentOf[cl.Self.Value] = *cl.Parent
		}
		e, err := normalizeAvailabilityZone(cl)
		if err != nil {
			continue
		}
		if region, ok := regionOf(cl.Self.Value, parentOf); ok {
			e.Relations = append(e.Relations, &pluginv1.ObservedRelation{
				Type: "in-region", ToScheme: "vcenter.datacenter.moref", ToValue: region,
			})
		}
		out = append(out, e)
	}

	// Hosts → host + the member-of edge to its cluster (availability-zone).
	hosts, err := retrieve[mo.HostSystem](ctx, m, root, "HostSystem", hostProps)
	if err != nil {
		return nil, err
	}
	for _, h := range hosts {
		e, err := normalizeHost(h)
		if err != nil {
			continue
		}
		if h.Summary.Hardware != nil && h.Summary.Hardware.Uuid != "" {
			hostUUIDByRef[h.Self.Value] = h.Summary.Hardware.Uuid
		}
		if h.Parent != nil && h.Parent.Type == "ClusterComputeResource" {
			e.Relations = append(e.Relations, &pluginv1.ObservedRelation{
				Type: "member-of", ToScheme: "vcenter.cluster.moref", ToValue: h.Parent.Value,
			})
		}
		out = append(out, e)
	}

	// VMs → vm + runs-on (to host) + placed-in (to network).
	machines, err := retrieve[mo.VirtualMachine](ctx, m, root, "VirtualMachine", vmProps)
	if err != nil {
		return nil, err
	}
	for _, vm := range machines {
		e, err := normalizeVM(vm)
		if err != nil {
			continue
		}
		// runs-on edge to the ESXi host, named BY IDENTITY (vcenter.host.uuid) — the plugin never sees
		// graph ids; the host resolves + stamps (ADR-0047 §1).
		if vm.Runtime.Host != nil {
			if uuid, ok := hostUUIDByRef[vm.Runtime.Host.Value]; ok {
				e.Relations = append(e.Relations, &pluginv1.ObservedRelation{
					Type: "runs-on", ToScheme: "vcenter.host.uuid", ToValue: uuid,
				})
			}
		}
		// placed-in edges to each attached network (ADR-0059 decision 2).
		for _, netRef := range vm.Network {
			e.Relations = append(e.Relations, &pluginv1.ObservedRelation{
				Type: "placed-in", ToScheme: "vcenter.network.moref", ToValue: netRef.Value,
			})
		}
		out = append(out, e)
	}
	return out, nil
}
