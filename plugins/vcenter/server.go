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
		TombstoneSchemes: []string{"vcenter.uuid", "vcenter.host.uuid", "vcenter.network.moref"},
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

// enumerate bulk-reads hosts + VMs and normalizes them. Pure content-expertise;
// no graph writes (the plugin holds no DB path).
func enumerate(ctx context.Context, c *vim25.Client) ([]*pluginv1.ObservedEntity, error) {
	m := view.NewManager(c)
	var out []*pluginv1.ObservedEntity
	hostUUIDByRef := map[string]string{} // host moref -> vcenter.host.uuid, for the runs-on edge

	// Networks (portgroups / DVPGs / opaque) → subnet Entities. vSphere is a network
	// Source; its portgroups join the estate alongside cloud subnets (ADR-0059/0060).
	nv, err := m.CreateContainerView(ctx, c.ServiceContent.RootFolder, []string{"Network"}, true)
	if err != nil {
		return nil, fmt.Errorf("vcenter: network view: %w", err)
	}
	defer nv.Destroy(ctx) //nolint:errcheck
	var networks []mo.Network
	if err := nv.Retrieve(ctx, []string{"Network"}, networkProps, &networks); err != nil {
		return nil, fmt.Errorf("vcenter: retrieve networks: %w", err)
	}
	for _, n := range networks {
		e, err := normalizeNetwork(n)
		if err != nil {
			continue
		}
		out = append(out, e)
	}

	hv, err := m.CreateContainerView(ctx, c.ServiceContent.RootFolder, []string{"HostSystem"}, true)
	if err != nil {
		return nil, fmt.Errorf("vcenter: host view: %w", err)
	}
	defer hv.Destroy(ctx) //nolint:errcheck
	var hosts []mo.HostSystem
	if err := hv.Retrieve(ctx, []string{"HostSystem"}, hostProps, &hosts); err != nil {
		return nil, fmt.Errorf("vcenter: retrieve hosts: %w", err)
	}
	for _, h := range hosts {
		e, err := normalizeHost(h)
		if err != nil {
			continue
		}
		out = append(out, e)
		if h.Summary.Hardware != nil && h.Summary.Hardware.Uuid != "" {
			hostUUIDByRef[h.Self.Value] = h.Summary.Hardware.Uuid
		}
	}

	vv, err := m.CreateContainerView(ctx, c.ServiceContent.RootFolder, []string{"VirtualMachine"}, true)
	if err != nil {
		return nil, fmt.Errorf("vcenter: vm view: %w", err)
	}
	defer vv.Destroy(ctx) //nolint:errcheck
	var machines []mo.VirtualMachine
	if err := vv.Retrieve(ctx, []string{"VirtualMachine"}, vmProps, &machines); err != nil {
		return nil, fmt.Errorf("vcenter: retrieve vms: %w", err)
	}
	for _, vm := range machines {
		e, err := normalizeVM(vm)
		if err != nil {
			continue
		}
		// runs-on edge to the ESXi host, named BY IDENTITY (vcenter.host.uuid) —
		// the plugin never sees graph ids; the host resolves + stamps (ADR-0047 §1).
		if vm.Runtime.Host != nil {
			if uuid, ok := hostUUIDByRef[vm.Runtime.Host.Value]; ok {
				e.Relations = append(e.Relations, &pluginv1.ObservedRelation{
					Type: "runs-on", ToScheme: "vcenter.host.uuid", ToValue: uuid,
				})
			}
		}
		// placed-in edges to each attached network (ADR-0059 decision 2): the VM sits
		// in these portgroups. Observed placement — a cloud Syncer emits the same edge
		// shape for its subnets, so "the VMs in network X" is one relation-aware View.
		for _, netRef := range vm.Network {
			e.Relations = append(e.Relations, &pluginv1.ObservedRelation{
				Type: "placed-in", ToScheme: "vcenter.network.moref", ToValue: netRef.Value,
			})
		}
		out = append(out, e)
	}
	return out, nil
}
