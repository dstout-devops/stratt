package vcenter

import (
	"encoding/json"
	"testing"

	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

// The transport is GATED ON WHAT vCENTER REPORTS, not on the fact that this is vSphere.
// vmware_tools executes guest operations through the vCenter API, so it works if and only if
// VMware Tools is RUNNING — and claiming it for every VM would be COMPUTING a reach fact from
// the substrate, which ADR-0142 D4 forbids in those words.
func TestVMwareToolsTransportIsObservedNotAssumed(t *testing.T) {
	vm := func(tools string) mo.VirtualMachine {
		m := mo.VirtualMachine{}
		m.Config = &types.VirtualMachineConfigInfo{
			Uuid:     "421f-abcd",
			Hardware: types.VirtualHardware{NumCPU: 2, MemoryMB: 4096},
		}
		if tools != "" {
			m.Guest = &types.GuestInfo{ToolsRunningStatus: tools}
		}
		return m
	}
	transportOf := func(t *testing.T, m mo.VirtualMachine) map[string]string {
		t.Helper()
		e, err := normalizeVM(m)
		if err != nil {
			t.Fatal(err)
		}
		raw, ok := e.GetFacets()["mgmt.transport"]
		if !ok {
			return nil
		}
		var out map[string]string
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// Tools RUNNING ⇒ the transport is claimed, keyed by the uuid this plugin already
	// identifies the Entity on, so the two cannot drift apart.
	got := transportOf(t, vm(string(types.VirtualMachineToolsRunningStatusGuestToolsRunning)))
	if got == nil || got["kind"] != "vmware_tools" || got["vmUuid"] != "421f-abcd" {
		t.Fatalf("a Tools-running VM must carry the transport: %v", got)
	}

	// Everything else ⇒ NO claim. A VM whose Tools are not running cannot be reached this
	// way, and saying otherwise sends a converge at a guest that will never answer.
	for _, status := range []string{
		string(types.VirtualMachineToolsRunningStatusGuestToolsNotRunning),
		string(types.VirtualMachineToolsRunningStatusGuestToolsExecutingScripts),
		"", // no guest info at all — a powered-off or just-created VM
	} {
		if got := transportOf(t, vm(status)); got != nil {
			t.Errorf("toolsRunningStatus %q must claim NO transport, got %v", status, got)
		}
	}
}
