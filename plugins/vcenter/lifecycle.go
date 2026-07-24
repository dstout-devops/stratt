// Entity-lifecycle Actions (ADR-0114): power / reconfigure / delete for an EXISTING vSphere VM, targeted
// by its vcenter.uuid — the same identity scheme the Syncer projects (ADR-0113 D1). These mirror awsec2's
// imperative lifecycle Actions (ADR-0095): identity-param targeting, invoke-only, narrow write-scope. The
// one new primitive vs awsec2 (which passes id strings straight to the API) is resolveVM — vcenter is the
// first delete/lifecycle Action on a Syncer-observed IDENTITY object, so it must resolve a govmomi handle
// by uuid first. State-changing ops project OUTPUTS ONLY; the Syncer stays the sole observed-state writer
// (ADR-0114 D3). delete-vm is outputs-only + idempotent-on-absence, and the graph tombstones by the next
// full-sync (ADR-0114 D2 / ADR-0042) — an InvokeResult cannot emit a tombstone.
//
// Each op is a PURE helper over a vim25 client (resolveVM/powerVM/reconfigureVM/deleteVM), tested against
// the in-process vcsim; the handlers are thin wiring (connect + stream + terminal), mirroring provision.go.
package vcenter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25"
	vimtypes "github.com/vmware/govmomi/vim25/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

const (
	actionPowerOff      = "vcenter/power-off"
	actionPowerOn       = "vcenter/power-on"
	actionReset         = "vcenter/reset"
	actionSuspend       = "vcenter/suspend"
	actionShutdownGuest = "vcenter/shutdown-guest"
	actionReconfigure   = "vcenter/reconfigure"
	actionDeleteVM      = "vcenter/delete-vm"
)

// errVMNotFound is the sentinel for a lifecycle op whose target uuid does not resolve (a real target
// miss, distinct from delete's idempotent already-gone).
var errVMNotFound = errors.New("no vm with that uuid")

// vmTarget is the decoded identity param every VM lifecycle Action carries (ADR-0114 D1): the VM's
// vcenter.uuid — the same scheme the Syncer keys on. Mirrors awsec2's {instanceId}.
type vmTarget struct {
	UUID string `json:"uuid"`
}

// reconfigureParams is vcenter/reconfigure's input: the target + the new sizing (either/both optional).
type reconfigureParams struct {
	UUID     string `json:"uuid"`
	CPUs     int32  `json:"cpus"`
	MemoryMB int64  `json:"memoryMB"`
}

// ── pure govmomi helpers (client-in, tested against the simulator) ───────────────────────────────────

// resolveVM resolves a VM handle by its vcenter.uuid (config.uuid = BIOS uuid, the scheme the Syncer
// projects). Searches ALL datacenters (the enterprise seed lays down several). A miss returns (nil, nil)
// — NOT an error — so a caller can treat "already gone" idempotently (ADR-0114 D2).
func resolveVM(ctx context.Context, c *vim25.Client, uuid string) (*object.VirtualMachine, error) {
	si := object.NewSearchIndex(c)
	instanceUUID := false // false ⇒ match config.uuid (BIOS uuid), not config.instanceUuid
	ref, err := si.FindByUuid(ctx, nil /* all datacenters */, uuid, true /* vmSearch */, &instanceUUID)
	if err != nil {
		return nil, fmt.Errorf("resolve vm by uuid %q: %w", uuid, err)
	}
	if ref == nil {
		return nil, nil // not found
	}
	vm, ok := ref.(*object.VirtualMachine)
	if !ok {
		return nil, fmt.Errorf("uuid %q resolved to %T, not a VirtualMachine", uuid, ref)
	}
	return vm, nil
}

// powerVM runs a power/state op on the VM and returns the resulting power state. errVMNotFound if the
// uuid does not resolve. op is one of the action* power constants.
func powerVM(ctx context.Context, c *vim25.Client, uuid, op string) (string, error) {
	vm, err := resolveVM(ctx, c, uuid)
	if err != nil {
		return "", err
	}
	if vm == nil {
		return "", errVMNotFound
	}
	var task *object.Task
	switch op {
	case actionPowerOff:
		task, err = vm.PowerOff(ctx)
	case actionPowerOn:
		task, err = vm.PowerOn(ctx)
	case actionReset:
		task, err = vm.Reset(ctx)
	case actionSuspend:
		task, err = vm.Suspend(ctx)
	case actionShutdownGuest:
		// Guest-tools op (no Task); needs running tools. A tools error surfaces to the caller.
		if gerr := vm.ShutdownGuest(ctx); gerr != nil {
			return "", gerr
		}
	default:
		return "", fmt.Errorf("powerVM: unhandled op %q", op)
	}
	if err != nil {
		return "", err
	}
	if task != nil {
		if _, werr := task.WaitForResult(ctx, nil); werr != nil {
			return "", werr
		}
	}
	state, _ := vm.PowerState(ctx) // best-effort read-back for the typed output
	return string(state), nil
}

// reconfigureVM changes a VM's cpu/memory (ReconfigVM_Task). errVMNotFound if the uuid does not resolve.
func reconfigureVM(ctx context.Context, c *vim25.Client, uuid string, cpus int32, memoryMB int64) error {
	vm, err := resolveVM(ctx, c, uuid)
	if err != nil {
		return err
	}
	if vm == nil {
		return errVMNotFound
	}
	spec := vimtypes.VirtualMachineConfigSpec{}
	if cpus > 0 {
		spec.NumCPUs = cpus
	}
	if memoryMB > 0 {
		spec.MemoryMB = memoryMB
	}
	task, err := vm.Reconfigure(ctx, spec)
	if err != nil {
		return err
	}
	_, err = task.WaitForResult(ctx, nil)
	return err
}

// deleteVM destroys a VM (Destroy_Task). Returns found=false (no error) if the uuid does not resolve —
// idempotent-on-absence (ADR-0114 D2), so a re-issue over the tombstone sync-lag window is a safe no-op.
func deleteVM(ctx context.Context, c *vim25.Client, uuid string) (found bool, err error) {
	vm, err := resolveVM(ctx, c, uuid)
	if err != nil {
		return false, err
	}
	if vm == nil {
		return false, nil // already gone
	}
	// vSphere refuses Destroy on a powered-on VM — power it off first (best-effort; a VM already off
	// or mid-transition is fine).
	if state, serr := vm.PowerState(ctx); serr == nil && state == vimtypes.VirtualMachinePowerStatePoweredOn {
		if pt, perr := vm.PowerOff(ctx); perr == nil {
			_, _ = pt.WaitForResult(ctx, nil)
		}
	}
	task, err := vm.Destroy(ctx)
	if err != nil {
		return true, err
	}
	_, err = task.WaitForResult(ctx, nil)
	return true, err
}

// ── handlers (thin wiring: connect + stream + terminal) ──────────────────────────────────────────────

func (s *Server) invokeVMPower(ctx context.Context, req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], op string) error {
	var t vmTarget
	if err := decodeArgs(req, &t); err != nil {
		return err
	}
	if t.UUID == "" {
		return status.Errorf(codes.InvalidArgument, "%s requires uuid", op)
	}
	if err := s.progress(stream, req, fmt.Sprintf("%s on vm %s", op, t.UUID)); err != nil {
		return err
	}
	if req.GetDryRun() {
		return s.terminalDryRun(stream, req, fmt.Sprintf("dry-run ok: would %s vm %s", op, t.UUID), op)
	}
	client, err := connect(ctx, s.cfg)
	if err != nil {
		return err
	}
	defer client.Logout(ctx) //nolint:errcheck
	state, err := powerVM(ctx, client.Client, t.UUID, op)
	if err != nil {
		if errors.Is(err, errVMNotFound) {
			return s.terminalFailure(stream, req, status.Errorf(codes.NotFound, "%s: no vm with uuid %s", op, t.UUID))
		}
		return s.terminalFailure(stream, req, fmt.Errorf("%s: %w", op, err))
	}
	outputs, _ := json.Marshal(map[string]any{"uuid": t.UUID, "powerState": state})
	return s.terminalOK(stream, req, op+" "+t.UUID, outputs, contractFor(op))
}

func (s *Server) invokeReconfigure(ctx context.Context, req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse]) error {
	var p reconfigureParams
	if err := decodeArgs(req, &p); err != nil {
		return err
	}
	if p.UUID == "" {
		return status.Errorf(codes.InvalidArgument, "%s requires uuid", actionReconfigure)
	}
	if p.CPUs == 0 && p.MemoryMB == 0 {
		return status.Errorf(codes.InvalidArgument, "%s requires at least one of cpus, memoryMB", actionReconfigure)
	}
	if err := s.progress(stream, req, fmt.Sprintf("reconfigure vm %s (cpus=%d memoryMB=%d)", p.UUID, p.CPUs, p.MemoryMB)); err != nil {
		return err
	}
	if req.GetDryRun() {
		return s.terminalDryRun(stream, req, "dry-run ok: would reconfigure vm "+p.UUID, actionReconfigure)
	}
	client, err := connect(ctx, s.cfg)
	if err != nil {
		return err
	}
	defer client.Logout(ctx) //nolint:errcheck
	if err := reconfigureVM(ctx, client.Client, p.UUID, p.CPUs, p.MemoryMB); err != nil {
		if errors.Is(err, errVMNotFound) {
			return s.terminalFailure(stream, req, status.Errorf(codes.NotFound, "%s: no vm with uuid %s", actionReconfigure, p.UUID))
		}
		return s.terminalFailure(stream, req, fmt.Errorf("%s: %w", actionReconfigure, err))
	}
	outputs, _ := json.Marshal(map[string]any{"uuid": p.UUID})
	return s.terminalOK(stream, req, "reconfigured "+p.UUID, outputs, contractFor(actionReconfigure))
}

func (s *Server) invokeDeleteVM(ctx context.Context, req *pluginv1.InvokeRequest, stream grpc.ServerStreamingServer[pluginv1.InvokeResponse]) error {
	var t vmTarget
	if err := decodeArgs(req, &t); err != nil {
		return err
	}
	if t.UUID == "" {
		return status.Errorf(codes.InvalidArgument, "%s requires uuid", actionDeleteVM)
	}
	if err := s.progress(stream, req, "delete vm "+t.UUID); err != nil {
		return err
	}
	if req.GetDryRun() {
		return s.terminalDryRun(stream, req, "dry-run ok: would delete vm "+t.UUID, actionDeleteVM)
	}
	client, err := connect(ctx, s.cfg)
	if err != nil {
		return err
	}
	defer client.Logout(ctx) //nolint:errcheck
	found, err := deleteVM(ctx, client.Client, t.UUID)
	if err != nil {
		return s.terminalFailure(stream, req, fmt.Errorf("%s: %w", actionDeleteVM, err))
	}
	outputs, _ := json.Marshal(map[string]any{"uuid": t.UUID})
	msg := "deleted " + t.UUID
	if !found {
		msg = "vm " + t.UUID + " already gone" // idempotent success (ADR-0114 D2)
	}
	return s.terminalOK(stream, req, msg, outputs, contractFor(actionDeleteVM))
}

// ── shared helpers ─────────────────────────────────────────────────────────────────────────────────

// decodeArgs unmarshals the InvokeRequest args into v (a §1.5 content-blind decode).
func decodeArgs(req *pluginv1.InvokeRequest, v any) error {
	if args := req.GetArgs(); args != nil && len(args.GetBytes()) > 0 {
		if err := json.Unmarshal(args.GetBytes(), v); err != nil {
			return status.Errorf(codes.InvalidArgument, "%s: invalid args: %v", req.GetAction(), err)
		}
	}
	return nil
}

// contractFor returns the output ContractRef schema id for an action ("vcenter/x" → "actions/vcenter/x.output").
func contractFor(action string) string { return "actions/" + action + ".output" }

func (s *Server) progress(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, msg string) error {
	return stream.Send(&pluginv1.InvokeResponse{Event: &pluginv1.TaskEvent{
		Level:         pluginv1.TaskEvent_LEVEL_INFO,
		Message:       msg,
		At:            timestamppb.Now(),
		CorrelationId: req.GetEnvelope().GetCorrelationId(),
	}})
}

func (s *Server) terminalOK(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, msg string, outputs []byte, outputContract string) error {
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
		},
	})
}

func (s *Server) terminalDryRun(stream grpc.ServerStreamingServer[pluginv1.InvokeResponse], req *pluginv1.InvokeRequest, msg, action string) error {
	return stream.Send(&pluginv1.InvokeResponse{
		Event: &pluginv1.TaskEvent{
			Level:         pluginv1.TaskEvent_LEVEL_INFO,
			Message:       msg,
			At:            timestamppb.Now(),
			CorrelationId: req.GetEnvelope().GetCorrelationId(),
			Terminal:      true,
			Ok:            true,
		},
		Result: &pluginv1.InvokeResult{OutputContract: &pluginv1.ContractRef{SchemaId: contractFor(action)}},
	})
}
