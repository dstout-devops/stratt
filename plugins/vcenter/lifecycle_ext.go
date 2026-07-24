// Extended lifecycle Actions (ADR-0114 slice 2): snapshot (create/revert/remove), mobility
// (migrate/clone), and DVS portgroup lifecycle (reconfigure/delete). Same imperative-Action pattern as
// lifecycle.go — identity-param targeting, pure govmomi helpers tested against in-process vcsim, thin
// handlers. clone is the D3 EXCEPTION: it CREATES a VM, so it projects the new VM identity-only (like
// create-vm). Snapshots are VM sub-objects (not graph Entities) → no projection. delete-portgroup is
// outputs-only + idempotent-on-absence (tombstone-by-absence, D2).
package vcenter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	vimtypes "github.com/vmware/govmomi/vim25/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

const (
	actionSnapshotCreate  = "vcenter/snapshot-create"
	actionSnapshotRevert  = "vcenter/snapshot-revert"
	actionSnapshotRemove  = "vcenter/snapshot-remove"
	actionMigrate         = "vcenter/migrate"
	actionClone           = "vcenter/clone"
	actionReconfigurePG   = "vcenter/reconfigure-portgroup"
	actionDeletePortgroup = "vcenter/delete-portgroup"
	pgObjType             = "DistributedVirtualPortgroup"
	errPGNotFoundText     = "no portgroup with that moref"
)

// ── snapshot ─────────────────────────────────────────────────────────────────────────────────────

func snapshotCreate(ctx context.Context, c *vim25.Client, uuid, name, description string, memory, quiesce bool) error {
	vm, err := resolveVM(ctx, c, uuid)
	if err != nil {
		return err
	}
	if vm == nil {
		return errVMNotFound
	}
	task, err := vm.CreateSnapshot(ctx, name, description, memory, quiesce)
	if err != nil {
		return err
	}
	_, err = task.WaitForResult(ctx, nil)
	return err
}

func snapshotRevert(ctx context.Context, c *vim25.Client, uuid, name string) error {
	vm, err := resolveVM(ctx, c, uuid)
	if err != nil {
		return err
	}
	if vm == nil {
		return errVMNotFound
	}
	task, err := vm.RevertToSnapshot(ctx, name, false)
	if err != nil {
		return err
	}
	_, err = task.WaitForResult(ctx, nil)
	return err
}

func snapshotRemove(ctx context.Context, c *vim25.Client, uuid, name string, removeChildren bool) error {
	vm, err := resolveVM(ctx, c, uuid)
	if err != nil {
		return err
	}
	if vm == nil {
		return errVMNotFound
	}
	task, err := vm.RemoveSnapshot(ctx, name, removeChildren, nil)
	if err != nil {
		return err
	}
	_, err = task.WaitForResult(ctx, nil)
	return err
}

// ── mobility ─────────────────────────────────────────────────────────────────────────────────────

// migrateVM moves a VM to a named host and/or resource pool (vMotion). Empty targets keep the current
// placement (a no-op relocate). Resolves host/pool in the VM's own datacenter.
func migrateVM(ctx context.Context, c *vim25.Client, uuid, hostName, poolName string) error {
	vm, err := resolveVM(ctx, c, uuid)
	if err != nil {
		return err
	}
	if vm == nil {
		return errVMNotFound
	}
	finder, err := finderForVM(ctx, c, vm)
	if err != nil {
		return err
	}
	var host *object.HostSystem
	var pool *object.ResourcePool
	if hostName != "" {
		if host, err = finder.HostSystem(ctx, hostName); err != nil {
			return fmt.Errorf("migrate: host %q: %w", hostName, err)
		}
	}
	if poolName != "" {
		if pool, err = finder.ResourcePool(ctx, poolName); err != nil {
			return fmt.Errorf("migrate: pool %q: %w", poolName, err)
		}
	}
	// Use RelocateVM_Task (the general vMotion primitive) rather than MigrateVM_Task — it is the broader
	// op and better supported by simulators. host/pool are set on the RelocateSpec.
	spec := vimtypes.VirtualMachineRelocateSpec{}
	if host != nil {
		ref := host.Reference()
		spec.Host = &ref
	}
	if pool != nil {
		ref := pool.Reference()
		spec.Pool = &ref
	}
	task, err := vm.Relocate(ctx, spec, vimtypes.VirtualMachineMovePriorityDefaultPriority)
	if err != nil {
		return err
	}
	_, err = task.WaitForResult(ctx, nil)
	return err
}

// cloneVM clones a VM into its own folder and returns the NEW VM's vcenter.uuid (the D3 create-exception:
// clone makes a new Entity, projected identity-only).
func cloneVM(ctx context.Context, c *vim25.Client, uuid, name string) (string, error) {
	vm, err := resolveVM(ctx, c, uuid)
	if err != nil {
		return "", err
	}
	if vm == nil {
		return "", errVMNotFound
	}
	var mvm mo.VirtualMachine
	if err := vm.Properties(ctx, vm.Reference(), []string{"parent"}, &mvm); err != nil {
		return "", fmt.Errorf("clone: read source parent: %w", err)
	}
	if mvm.Parent == nil {
		return "", fmt.Errorf("clone: source vm has no parent folder")
	}
	folder := object.NewFolder(c, *mvm.Parent)
	spec := vimtypes.VirtualMachineCloneSpec{
		Location: vimtypes.VirtualMachineRelocateSpec{},
		PowerOn:  false,
		Template: false,
	}
	task, err := vm.Clone(ctx, folder, name, spec)
	if err != nil {
		return "", err
	}
	info, err := task.WaitForResult(ctx, nil)
	if err != nil {
		return "", err
	}
	ref, ok := info.Result.(vimtypes.ManagedObjectReference)
	if !ok {
		return "", fmt.Errorf("clone: unexpected task result %T", info.Result)
	}
	newVM := object.NewVirtualMachine(c, ref)
	var nmvm mo.VirtualMachine
	if err := newVM.Properties(ctx, ref, []string{"config.uuid"}, &nmvm); err != nil {
		return "", fmt.Errorf("clone: read new uuid: %w", err)
	}
	if nmvm.Config == nil || nmvm.Config.Uuid == "" {
		return "", fmt.Errorf("clone: new vm has no uuid")
	}
	return nmvm.Config.Uuid, nil
}

// finderForVM returns a finder scoped to a datacenter for resolving host/pool names on migrate. Uses
// the default (sole) datacenter; falls back to the first when several exist (a multi-DC migrate should
// carry an explicit datacenter — booked, ADR-0114 follow-up).
func finderForVM(ctx context.Context, c *vim25.Client, _ *object.VirtualMachine) (*find.Finder, error) {
	finder := find.NewFinder(c, true)
	if dc, err := finder.DefaultDatacenter(ctx); err == nil {
		finder.SetDatacenter(dc)
		return finder, nil
	}
	dcs, err := finder.DatacenterList(ctx, "*")
	if err != nil || len(dcs) == 0 {
		return nil, fmt.Errorf("migrate: no datacenter to resolve targets: %w", err)
	}
	finder.SetDatacenter(dcs[0])
	return finder, nil
}

// ── portgroup lifecycle ──────────────────────────────────────────────────────────────────────────

// resolvePortgroup builds a DVS portgroup handle from its moref (vcenter.network.moref). Returns
// (nil,nil) if it no longer exists (idempotent-on-absence for delete).
func resolvePortgroup(ctx context.Context, c *vim25.Client, moref string) (*object.DistributedVirtualPortgroup, *mo.DistributedVirtualPortgroup, error) {
	ref := vimtypes.ManagedObjectReference{Type: pgObjType, Value: moref}
	pg := object.NewDistributedVirtualPortgroup(c, ref)
	var mpg mo.DistributedVirtualPortgroup
	if err := pg.Properties(ctx, ref, []string{"config"}, &mpg); err != nil {
		if isNotFound(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("resolve portgroup %q: %w", moref, err)
	}
	return pg, &mpg, nil
}

// reconfigurePortgroup sets a portgroup's 802.1Q VLAN. Unlike create, this needs the current
// ConfigVersion (optimistic concurrency) read back from the object.
func reconfigurePortgroup(ctx context.Context, c *vim25.Client, moref string, vlan int32) error {
	pg, mpg, err := resolvePortgroup(ctx, c, moref)
	if err != nil {
		return err
	}
	if pg == nil {
		return fmt.Errorf("%s", errPGNotFoundText)
	}
	spec := vimtypes.DVPortgroupConfigSpec{
		ConfigVersion: mpg.Config.ConfigVersion,
		DefaultPortConfig: &vimtypes.VMwareDVSPortSetting{
			Vlan: &vimtypes.VmwareDistributedVirtualSwitchVlanIdSpec{VlanId: vlan},
		},
	}
	task, err := pg.Reconfigure(ctx, spec)
	if err != nil {
		return err
	}
	_, err = task.WaitForResult(ctx, nil)
	return err
}

// deletePortgroup destroys a DVS portgroup. found=false (no error) if already gone (idempotent, D2).
func deletePortgroup(ctx context.Context, c *vim25.Client, moref string) (found bool, err error) {
	pg, _, err := resolvePortgroup(ctx, c, moref)
	if err != nil {
		return false, err
	}
	if pg == nil {
		return false, nil // already gone
	}
	task, err := pg.Destroy(ctx)
	if err != nil {
		return true, err
	}
	_, err = task.WaitForResult(ctx, nil)
	return true, err
}

// ── handlers ─────────────────────────────────────────────────────────────────────────────────────

type snapshotCreateParams struct {
	UUID        string `json:"uuid"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Memory      bool   `json:"memory"`
	Quiesce     bool   `json:"quiesce"`
}

type snapshotOpParams struct {
	UUID           string `json:"uuid"`
	Name           string `json:"name"`
	RemoveChildren bool   `json:"removeChildren"`
}

type migrateParams struct {
	UUID string `json:"uuid"`
	Host string `json:"host"`
	Pool string `json:"pool"`
}

type cloneParams struct {
	UUID          string            `json:"uuid"`
	Name          string            `json:"name"`
	ProjectKind   string            `json:"projectKind"`
	ProjectLabels map[string]string `json:"projectLabels"`
}

type pgReconfigureParams struct {
	Moref  string `json:"moref"`
	VLANID int32  `json:"vlanId"`
}

type pgTarget struct {
	Moref string `json:"moref"`
}

// notFoundOrFail maps a lifecycle helper error to a terminal NotFound (missing target) or a generic
// terminal failure — the shared tail of the VM-op handlers.
func (s *Server) notFoundOrFail(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, action string, err error) error {
	if errors.Is(err, errVMNotFound) {
		return s.terminalFailure(stream, req, status.Errorf(codes.NotFound, "%s: target uuid not found", action))
	}
	return s.terminalFailure(stream, req, fmt.Errorf("%s: %w", action, err))
}

func (s *Server) invokeSnapshotCreate(ctx context.Context, req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse]) error {
	var p snapshotCreateParams
	if err := decodeArgs(req, &p); err != nil {
		return err
	}
	if p.UUID == "" || p.Name == "" {
		return status.Errorf(codes.InvalidArgument, "%s requires uuid and name", actionSnapshotCreate)
	}
	if err := s.progress(stream, req, fmt.Sprintf("snapshot %q of vm %s", p.Name, p.UUID)); err != nil {
		return err
	}
	if req.GetDryRun() {
		return s.terminalDryRun(stream, req, "dry-run ok: would snapshot vm "+p.UUID, actionSnapshotCreate)
	}
	client, err := connect(ctx, s.cfg)
	if err != nil {
		return err
	}
	defer client.Logout(ctx) //nolint:errcheck
	if err := snapshotCreate(ctx, client.Client, p.UUID, p.Name, p.Description, p.Memory, p.Quiesce); err != nil {
		return s.notFoundOrFail(stream, req, actionSnapshotCreate, err)
	}
	outputs, _ := json.Marshal(map[string]any{"uuid": p.UUID, "snapshot": p.Name})
	return s.terminalOK(stream, req, "snapshotted "+p.UUID, outputs, contractFor(actionSnapshotCreate))
}

func (s *Server) invokeSnapshotOp(ctx context.Context, req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], action string) error {
	var p snapshotOpParams
	if err := decodeArgs(req, &p); err != nil {
		return err
	}
	if p.UUID == "" || p.Name == "" {
		return status.Errorf(codes.InvalidArgument, "%s requires uuid and name", action)
	}
	if err := s.progress(stream, req, fmt.Sprintf("%s %q on vm %s", action, p.Name, p.UUID)); err != nil {
		return err
	}
	if req.GetDryRun() {
		return s.terminalDryRun(stream, req, "dry-run ok: would "+action+" vm "+p.UUID, action)
	}
	client, err := connect(ctx, s.cfg)
	if err != nil {
		return err
	}
	defer client.Logout(ctx) //nolint:errcheck
	switch action {
	case actionSnapshotRevert:
		err = snapshotRevert(ctx, client.Client, p.UUID, p.Name)
	case actionSnapshotRemove:
		err = snapshotRemove(ctx, client.Client, p.UUID, p.Name, p.RemoveChildren)
	default:
		return status.Errorf(codes.Internal, "invokeSnapshotOp: unhandled %q", action)
	}
	if err != nil {
		return s.notFoundOrFail(stream, req, action, err)
	}
	outputs, _ := json.Marshal(map[string]any{"uuid": p.UUID, "snapshot": p.Name})
	return s.terminalOK(stream, req, action+" "+p.UUID, outputs, contractFor(action))
}

func (s *Server) invokeMigrate(ctx context.Context, req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse]) error {
	var p migrateParams
	if err := decodeArgs(req, &p); err != nil {
		return err
	}
	if p.UUID == "" {
		return status.Errorf(codes.InvalidArgument, "%s requires uuid", actionMigrate)
	}
	if err := s.progress(stream, req, fmt.Sprintf("migrate vm %s (host=%q pool=%q)", p.UUID, p.Host, p.Pool)); err != nil {
		return err
	}
	if req.GetDryRun() {
		return s.terminalDryRun(stream, req, "dry-run ok: would migrate vm "+p.UUID, actionMigrate)
	}
	client, err := connect(ctx, s.cfg)
	if err != nil {
		return err
	}
	defer client.Logout(ctx) //nolint:errcheck
	if err := migrateVM(ctx, client.Client, p.UUID, p.Host, p.Pool); err != nil {
		return s.notFoundOrFail(stream, req, actionMigrate, err)
	}
	outputs, _ := json.Marshal(map[string]any{"uuid": p.UUID})
	return s.terminalOK(stream, req, "migrated "+p.UUID, outputs, contractFor(actionMigrate))
}

// invokeClone is the D3 create-exception: it makes a NEW VM, so it projects the new VM IDENTITY-ONLY
// (keyed by the fresh vcenter.uuid + the estate overlay), exactly like create-vm.
func (s *Server) invokeClone(ctx context.Context, req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse]) error {
	var p cloneParams
	if err := decodeArgs(req, &p); err != nil {
		return err
	}
	if p.UUID == "" || p.Name == "" {
		return status.Errorf(codes.InvalidArgument, "%s requires uuid (source) and name (new vm)", actionClone)
	}
	if err := s.progress(stream, req, fmt.Sprintf("clone vm %s -> %q", p.UUID, p.Name)); err != nil {
		return err
	}
	if req.GetDryRun() {
		return s.terminalDryRun(stream, req, "dry-run ok: would clone vm "+p.UUID, actionClone)
	}
	client, err := connect(ctx, s.cfg)
	if err != nil {
		return err
	}
	defer client.Logout(ctx) //nolint:errcheck
	newUUID, err := cloneVM(ctx, client.Client, p.UUID, p.Name)
	if err != nil {
		return s.notFoundOrFail(stream, req, actionClone, err)
	}
	outputs, _ := json.Marshal(map[string]any{"uuid": newUUID})
	entity := buildEntity(createVMParams{Name: p.Name, ProjectKind: p.ProjectKind, ProjectLabels: p.ProjectLabels}, vmResult{UUID: newUUID})
	return s.terminalWithEntity(stream, req, "cloned "+p.UUID+" -> "+newUUID, outputs, contractFor(actionClone), entity)
}

func (s *Server) invokeReconfigurePortgroup(ctx context.Context, req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse]) error {
	var p pgReconfigureParams
	if err := decodeArgs(req, &p); err != nil {
		return err
	}
	if p.Moref == "" {
		return status.Errorf(codes.InvalidArgument, "%s requires moref", actionReconfigurePG)
	}
	if p.VLANID < 1 || p.VLANID > 4094 {
		return status.Errorf(codes.InvalidArgument, "%s requires a valid 802.1Q vlanId (1-4094), got %d", actionReconfigurePG, p.VLANID)
	}
	if err := s.progress(stream, req, fmt.Sprintf("reconfigure portgroup %s -> VLAN %d", p.Moref, p.VLANID)); err != nil {
		return err
	}
	if req.GetDryRun() {
		return s.terminalDryRun(stream, req, "dry-run ok: would reconfigure portgroup "+p.Moref, actionReconfigurePG)
	}
	client, err := connect(ctx, s.cfg)
	if err != nil {
		return err
	}
	defer client.Logout(ctx) //nolint:errcheck
	if err := reconfigurePortgroup(ctx, client.Client, p.Moref, p.VLANID); err != nil {
		return s.terminalFailure(stream, req, fmt.Errorf("%s: %w", actionReconfigurePG, err))
	}
	outputs, _ := json.Marshal(map[string]any{"moref": p.Moref})
	return s.terminalOK(stream, req, "reconfigured portgroup "+p.Moref, outputs, contractFor(actionReconfigurePG))
}

func (s *Server) invokeDeletePortgroup(ctx context.Context, req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse]) error {
	var p pgTarget
	if err := decodeArgs(req, &p); err != nil {
		return err
	}
	if p.Moref == "" {
		return status.Errorf(codes.InvalidArgument, "%s requires moref", actionDeletePortgroup)
	}
	if err := s.progress(stream, req, "delete portgroup "+p.Moref); err != nil {
		return err
	}
	if req.GetDryRun() {
		return s.terminalDryRun(stream, req, "dry-run ok: would delete portgroup "+p.Moref, actionDeletePortgroup)
	}
	client, err := connect(ctx, s.cfg)
	if err != nil {
		return err
	}
	defer client.Logout(ctx) //nolint:errcheck
	found, err := deletePortgroup(ctx, client.Client, p.Moref)
	if err != nil {
		return s.terminalFailure(stream, req, fmt.Errorf("%s: %w", actionDeletePortgroup, err))
	}
	outputs, _ := json.Marshal(map[string]any{"moref": p.Moref})
	msg := "deleted portgroup " + p.Moref
	if !found {
		msg = "portgroup " + p.Moref + " already gone"
	}
	return s.terminalOK(stream, req, msg, outputs, contractFor(actionDeletePortgroup))
}

// terminalWithEntity is terminalOK plus an identity-only ObservedEntity (for clone, the D3 exception).
func (s *Server) terminalWithEntity(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, msg string, outputs []byte, outputContract string, entity *pluginv1.ObservedEntity) error {
	return stream.Send(&pluginv1.InvokeResponse{
		Event: &pluginv1.TaskEvent{
			Level:         pluginv1.TaskEvent_LEVEL_INFO,
			Message:       msg,
			At:            timestamppb.Now(),
			CorrelationId: req.GetEnvelope().GetCorrelationId(),
			Terminal:      true,
			Ok:            true,
		},
		Result: &pluginv1.InvokeResult{
			Outputs:        &pluginv1.Payload{Bytes: outputs},
			OutputContract: &pluginv1.ContractRef{SchemaId: outputContract},
			Entities:       []*pluginv1.ObservedEntity{entity},
		},
	})
}

// isNotFound reports a vSphere ManagedObjectNotFound/NotFound fault (the object was destroyed) — so a
// resolve of a gone portgroup is idempotent-absence rather than a hard error (ADR-0114 D2).
func isNotFound(err error) bool {
	if err == nil || !soap.IsSoapFault(err) {
		return false
	}
	switch soap.ToSoapFault(err).VimFault().(type) {
	case vimtypes.ManagedObjectNotFound, vimtypes.NotFound:
		return true
	default:
		return false
	}
}
