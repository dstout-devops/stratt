// The `provisioning` capability provider (ADR-0113): vSphere VM build. This adds the INVOKE verb +
// a `vcenter/create-vm` Action to the (otherwise Syncer-only) vcenter plugin — the dual-verb shape
// ADR-0060 blessed (as netbox `ipam` did). The Action creates + powers on a VM via govmomi and
// projects it back BY IDENTITY ONLY (ADR-0112 D5 / ADR-0113 D3): `{kind, identityKeys, labels}` keyed
// on `vcenter.uuid` — the SAME scheme this plugin's Syncer OBSERVEs on — with a Run-provenance
// overlay, and NO Facet. `vm.config`/`vm.runtime` remain the Syncer's OBSERVE projection, so the
// build never becomes a second writer (§1.2). One module owns both verbs, so the OBSERVE↔build
// identity correlation is structural, not a cross-plugin convention.
package vcenter

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/mo"
	vimtypes "github.com/vmware/govmomi/vim25/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

const actionCreateVM = "vcenter/create-vm"

// createVMParams is the decoded actions/vcenter/create-vm.input (ADR-0113). ProjectKind/ProjectLabels
// are the estate OVERLAY the provisioning seam supplies (ADR-0058 D6): the built VM projects AS that
// estate kind with those labels (Run provenance, never a reconcile write, §1.2), so a fleet View
// selects it and its provisioning Finding resolves on the correlation label.
type createVMParams struct {
	Name          string            `json:"name"`
	Datacenter    string            `json:"datacenter"` // optional; default = the sole datacenter
	CPUs          int32             `json:"cpus"`
	MemoryMB      int64             `json:"memoryMB"`
	GuestID       string            `json:"guestId"` // optional; default "otherGuest"
	ProjectKind   string            `json:"projectKind"`
	ProjectLabels map[string]string `json:"projectLabels"`
}

// vmResult is a built VM's stable identity — the only thing the build projects (ADR-0113 D3).
type vmResult struct {
	UUID  string // config.uuid → the vcenter.uuid identity scheme the Syncer keys on
	Moref string // managed object ref (diagnostic output; not the identity)
}

// provisionVM creates + powers on a VM via govmomi and returns its stable identity. Pure
// content-expertise over a vim25 client — no graph writes (the plugin holds no DB path). Tested
// against the in-process vcsim simulator, the same backend the Syncer tests use.
func provisionVM(ctx context.Context, c *vim25.Client, p createVMParams) (vmResult, error) {
	finder := find.NewFinder(c, true)
	var (
		dc  *object.Datacenter
		err error
	)
	if p.Datacenter != "" {
		dc, err = finder.Datacenter(ctx, p.Datacenter)
	} else {
		dc, err = finder.DefaultDatacenter(ctx)
	}
	if err != nil {
		return vmResult{}, fmt.Errorf("datacenter: %w", err)
	}
	finder.SetDatacenter(dc)
	folders, err := dc.Folders(ctx)
	if err != nil {
		return vmResult{}, fmt.Errorf("folders: %w", err)
	}
	pools, err := finder.ResourcePoolList(ctx, "*")
	if err != nil || len(pools) == 0 {
		return vmResult{}, fmt.Errorf("resource pool: %w", err)
	}
	dss, err := finder.DatastoreList(ctx, "*")
	if err != nil || len(dss) == 0 {
		return vmResult{}, fmt.Errorf("datastore: %w", err)
	}
	guestID := p.GuestID
	if guestID == "" {
		guestID = "otherGuest"
	}
	spec := vimtypes.VirtualMachineConfigSpec{
		Name:     p.Name,
		GuestId:  guestID,
		NumCPUs:  p.CPUs,
		MemoryMB: p.MemoryMB,
		Files:    &vimtypes.VirtualMachineFileInfo{VmPathName: "[" + dss[0].Name() + "]"},
	}
	task, err := folders.VmFolder.CreateVM(ctx, spec, pools[0], nil)
	if err != nil {
		return vmResult{}, fmt.Errorf("create vm: %w", err)
	}
	info, err := task.WaitForResult(ctx, nil)
	if err != nil {
		return vmResult{}, fmt.Errorf("create vm task: %w", err)
	}
	ref, ok := info.Result.(vimtypes.ManagedObjectReference)
	if !ok {
		return vmResult{}, fmt.Errorf("create vm: unexpected task result %T", info.Result)
	}
	vm := object.NewVirtualMachine(c, ref)
	pt, err := vm.PowerOn(ctx)
	if err != nil {
		return vmResult{}, fmt.Errorf("power on: %w", err)
	}
	if _, err := pt.WaitForResult(ctx, nil); err != nil {
		return vmResult{}, fmt.Errorf("power on task: %w", err)
	}
	var mvm mo.VirtualMachine
	if err := vm.Properties(ctx, ref, []string{"config.uuid"}, &mvm); err != nil {
		return vmResult{}, fmt.Errorf("vm props: %w", err)
	}
	if mvm.Config == nil || mvm.Config.Uuid == "" {
		return vmResult{}, fmt.Errorf("created vm has no config.uuid — Syncer identity would be empty")
	}
	return vmResult{UUID: mvm.Config.Uuid, Moref: ref.Value}, nil
}

// buildEntity is the IDENTITY-ONLY build projection (ADR-0113 D3 / ADR-0112 D5): `{kind,
// identityKeys, labels}` keyed on vcenter.uuid — the SAME scheme the Syncer OBSERVEs on — with the
// estate overlay. It carries NO Facet: vm.config/vm.runtime remain the Syncer's OBSERVE projection,
// so the build never becomes a second/fourth writer (§1.2). Pure, so the invariant is unit-tested.
func buildEntity(p createVMParams, res vmResult) *pluginv1.ObservedEntity {
	kind := "vm"
	if p.ProjectKind != "" {
		kind = p.ProjectKind
	}
	labels := map[string]string{"source": "vsphere"}
	for k, v := range p.ProjectLabels {
		labels[k] = v
	}
	return &pluginv1.ObservedEntity{
		Kind:         kind,
		IdentityKeys: map[string]string{"vcenter.uuid": res.UUID},
		Labels:       labels,
		// No Facets — the Syncer owns vm.config/vm.runtime by OBSERVE (ADR-0113 D3).
	}
}

// Invoke is the content-blind Action dispatch (§1.5): an action this plugin does not ship is
// rejected, never guessed.
func (s *Server) Invoke(req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse]) error {
	// Reject BEFORE touching the stream — an unshipped action does no work (§1.5).
	switch req.GetAction() {
	case actionCreateVM:
		return s.invokeCreateVM(stream.Context(), req, stream)
	default:
		return status.Errorf(codes.InvalidArgument, "vcenter: unknown action %q", req.GetAction())
	}
}

func (s *Server) invokeCreateVM(ctx context.Context, req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse]) error {
	var p createVMParams
	if args := req.GetArgs(); args != nil && len(args.GetBytes()) > 0 {
		if err := json.Unmarshal(args.GetBytes(), &p); err != nil {
			return status.Errorf(codes.InvalidArgument, "vcenter/create-vm: invalid args: %v", err)
		}
	}
	if p.Name == "" {
		return status.Errorf(codes.InvalidArgument, "vcenter/create-vm requires name")
	}
	if p.CPUs == 0 {
		p.CPUs = 1
	}
	if p.MemoryMB == 0 {
		p.MemoryMB = 1024
	}

	client, err := connect(ctx, s.cfg)
	if err != nil {
		return err
	}
	defer client.Logout(ctx) //nolint:errcheck

	if err := stream.Send(&pluginv1.InvokeResponse{Event: &pluginv1.TaskEvent{
		Level:         pluginv1.TaskEvent_LEVEL_INFO,
		Message:       fmt.Sprintf("provisioning vSphere VM %q (%d vCPU, %d MB)", p.Name, p.CPUs, p.MemoryMB),
		At:            timestamppb.Now(),
		CorrelationId: req.GetEnvelope().GetCorrelationId(),
		Fields:        map[string]string{"name": p.Name},
	}}); err != nil {
		return err
	}

	// A dry-run passes the plan without creating anything — no bindable identity, no Entity.
	if req.GetDryRun() {
		return stream.Send(&pluginv1.InvokeResponse{
			Event: &pluginv1.TaskEvent{
				Level:         pluginv1.TaskEvent_LEVEL_INFO,
				Message:       fmt.Sprintf("dry-run ok: would provision VM %q", p.Name),
				At:            timestamppb.Now(),
				CorrelationId: req.GetEnvelope().GetCorrelationId(),
				Terminal:      true,
				Ok:            true,
			},
			Result: &pluginv1.InvokeResult{
				OutputContract: &pluginv1.ContractRef{SchemaId: "actions/vcenter/create-vm.output"},
			},
		})
	}

	res, err := provisionVM(ctx, client.Client, p)
	if err != nil {
		return s.terminalFailure(stream, req, fmt.Errorf("vcenter/create-vm: %w", err))
	}

	outputs, err := json.Marshal(map[string]any{"uuid": res.UUID, "moref": res.Moref})
	if err != nil {
		return s.terminalFailure(stream, req, fmt.Errorf("vcenter/create-vm: marshal outputs: %w", err))
	}

	entity := buildEntity(p, res)

	s.log.Info("provisioned vm", "name", p.Name, "vcenter.uuid", res.UUID, "moref", res.Moref)
	return stream.Send(&pluginv1.InvokeResponse{
		Event: &pluginv1.TaskEvent{
			Level:         pluginv1.TaskEvent_LEVEL_INFO,
			Message:       "provisioned " + p.Name,
			At:            timestamppb.Now(),
			CorrelationId: req.GetEnvelope().GetCorrelationId(),
			Fields:        map[string]string{"vcenter.uuid": res.UUID, "moref": res.Moref},
			Terminal:      true,
			Ok:            true,
		},
		Result: &pluginv1.InvokeResult{
			Outputs:        &pluginv1.Payload{Bytes: outputs},
			OutputContract: &pluginv1.ContractRef{SchemaId: "actions/vcenter/create-vm.output"},
			Entities:       []*pluginv1.ObservedEntity{entity},
		},
	})
}

func (s *Server) terminalFailure(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, cause error) error {
	s.log.Error("action failed", "error", cause)
	return stream.Send(&pluginv1.InvokeResponse{Event: &pluginv1.TaskEvent{
		Level:         pluginv1.TaskEvent_LEVEL_ERROR,
		Message:       cause.Error(),
		At:            timestamppb.Now(),
		CorrelationId: req.GetEnvelope().GetCorrelationId(),
		Terminal:      true,
		Ok:            false,
	}})
}
