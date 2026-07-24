package vcenter

import (
	"context"
	"errors"
	"testing"

	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/mo"
)

// mkVM creates a VM via the provisioning helper and returns its vcenter.uuid.
func mkVM(ctx context.Context, t *testing.T, c *vim25.Client, name string, cpus int32, mem int64) string {
	t.Helper()
	res, err := provisionVM(ctx, c, createVMParams{Name: name, CPUs: cpus, MemoryMB: mem})
	if err != nil {
		t.Fatalf("provisionVM %s: %v", name, err)
	}
	return res.UUID
}

// vmObserved reports whether the Syncer's enumerate projects a vm at the given vcenter.uuid.
func vmObserved(ctx context.Context, t *testing.T, c *vim25.Client, uuid string) bool {
	t.Helper()
	ents, err := enumerate(ctx, c)
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	for _, e := range ents {
		if e.GetKind() == "vm" && e.GetIdentityKeys()["vcenter.uuid"] == uuid {
			return true
		}
	}
	return false
}

// TestResolveVM proves the one new primitive (ADR-0114 D1): resolve a govmomi handle by the Syncer's own
// vcenter.uuid (BIOS config.uuid). A hit returns the VM; a miss returns (nil,nil), never an error.
func TestResolveVM(t *testing.T) {
	simulator.Test(func(ctx context.Context, c *vim25.Client) {
		uuid := mkVM(ctx, t, c, "res-01", 1, 512)
		vm, err := resolveVM(ctx, c, uuid)
		if err != nil {
			t.Fatalf("resolveVM hit: %v", err)
		}
		if vm == nil {
			t.Fatalf("resolveVM did not find the VM at uuid %s", uuid)
		}
		miss, err := resolveVM(ctx, c, "00000000-0000-0000-0000-000000000000")
		if err != nil {
			t.Fatalf("resolveVM miss must not error: %v", err)
		}
		if miss != nil {
			t.Errorf("resolveVM of a bogus uuid must return nil, got %v", miss)
		}
	})
}

// TestDeleteVMTombstonesByAbsence is the ADR-0114 D2 proof: after delete, the Syncer's enumerate no
// longer projects the uuid (so a full-sync TombstoneAbsent retracts it). Delete is also idempotent on a
// second call (already-gone), and on a never-existed uuid.
func TestDeleteVMTombstonesByAbsence(t *testing.T) {
	simulator.Test(func(ctx context.Context, c *vim25.Client) {
		uuid := mkVM(ctx, t, c, "del-01", 1, 512)
		if !vmObserved(ctx, t, c, uuid) {
			t.Fatalf("precondition: the built VM should be observed at %s", uuid)
		}
		found, err := deleteVM(ctx, c, uuid)
		if err != nil || !found {
			t.Fatalf("deleteVM: found=%v err=%v", found, err)
		}
		if vmObserved(ctx, t, c, uuid) {
			t.Errorf("after delete, the Syncer must NOT observe uuid %s (tombstone-by-absence)", uuid)
		}
		// Idempotent: deleting an already-gone / never-existed uuid is a no-op success.
		again, err := deleteVM(ctx, c, uuid)
		if err != nil {
			t.Fatalf("idempotent re-delete must not error: %v", err)
		}
		if again {
			t.Errorf("re-deleting a gone VM must report found=false, got true")
		}
		if _, err := deleteVM(ctx, c, "no-such-uuid"); err != nil {
			t.Errorf("deleting a never-existed uuid must be a no-op success, got %v", err)
		}
	})
}

// TestReconfigureVMObserved: after reconfigure, the VM's new sizing is present (the Syncer re-observes
// vm.config next poll; here we assert the underlying govmomi config directly).
func TestReconfigureVMObserved(t *testing.T) {
	simulator.Test(func(ctx context.Context, c *vim25.Client) {
		uuid := mkVM(ctx, t, c, "cfg-01", 1, 512)
		if err := reconfigureVM(ctx, c, uuid, 4, 4096); err != nil {
			t.Fatalf("reconfigureVM: %v", err)
		}
		vm, err := resolveVM(ctx, c, uuid)
		if err != nil || vm == nil {
			t.Fatalf("re-resolve: vm=%v err=%v", vm, err)
		}
		var mvm mo.VirtualMachine
		if err := vm.Properties(ctx, vm.Reference(), []string{"config.hardware"}, &mvm); err != nil {
			t.Fatalf("props: %v", err)
		}
		if mvm.Config.Hardware.NumCPU != 4 || mvm.Config.Hardware.MemoryMB != 4096 {
			t.Errorf("reconfigure not applied: got cpus=%d memMB=%d, want 4/4096",
				mvm.Config.Hardware.NumCPU, mvm.Config.Hardware.MemoryMB)
		}
	})
}

// TestPowerVMTransitions: power-off then power-on drive the observable power state.
func TestPowerVMTransitions(t *testing.T) {
	simulator.Test(func(ctx context.Context, c *vim25.Client) {
		uuid := mkVM(ctx, t, c, "pwr-01", 1, 512) // provisionVM powers it on
		state, err := powerVM(ctx, c, uuid, actionPowerOff)
		if err != nil {
			t.Fatalf("power-off: %v", err)
		}
		if state != "poweredOff" {
			t.Errorf("after power-off, want poweredOff, got %q", state)
		}
		state, err = powerVM(ctx, c, uuid, actionPowerOn)
		if err != nil {
			t.Fatalf("power-on: %v", err)
		}
		if state != "poweredOn" {
			t.Errorf("after power-on, want poweredOn, got %q", state)
		}
	})
}

// TestPowerVMNotFound: a lifecycle op on a non-existent uuid returns the errVMNotFound sentinel (mapped
// to NotFound at the handler), NOT idempotent success — only delete is idempotent-on-absence.
func TestPowerVMNotFound(t *testing.T) {
	simulator.Test(func(ctx context.Context, c *vim25.Client) {
		_, err := powerVM(ctx, c, "no-such-uuid", actionPowerOff)
		if !errors.Is(err, errVMNotFound) {
			t.Errorf("power op on a missing uuid must return errVMNotFound, got %v", err)
		}
	})
}
