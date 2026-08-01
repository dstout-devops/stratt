package vcenter

import (
	"encoding/json"
	"testing"

	"github.com/vmware/govmomi/vim25/mo"
	vimtypes "github.com/vmware/govmomi/vim25/types"
)

// The reach coordinate is a NAME first. These pin the ordering, because the whole point of
// ADR-0143 is that a vSphere VM becomes targetable by something that survives its address
// changing — and because the ordering is a decision, not an implementation detail.
func TestReachCoordinate(t *testing.T) {
	cases := []struct {
		name  string
		guest *vimtypes.GuestInfo
		want  string
	}{
		{
			name:  "a dotted guest name wins over the address",
			guest: &vimtypes.GuestInfo{HostName: "web-01.dev.stratt.test", IpAddress: "10.30.1.7"},
			want:  "web-01.dev.stratt.test",
		},
		{
			name:  "the name is lowercased — DNS is case-insensitive, the graph should not be",
			guest: &vimtypes.GuestInfo{HostName: "WEB-01.Dev.Stratt.Test"},
			want:  "web-01.dev.stratt.test",
		},
		{
			// A bare hostname's resolvability depends on search domains we neither control
			// nor observe. Promising it would be guessing; the address at least routes.
			name:  "a BARE hostname is not used — the address is",
			guest: &vimtypes.GuestInfo{HostName: "web-01", IpAddress: "10.30.1.7"},
			want:  "10.30.1.7",
		},
		{
			name:  "address only",
			guest: &vimtypes.GuestInfo{IpAddress: "10.30.1.7"},
			want:  "10.30.1.7",
		},
		{
			// "Built, not yet reachable" is a real and honest state — a VM with no tools, or
			// mid-boot. Guessing would make an unreachable host look reachable and fail the
			// next Run far from the cause (§1.8).
			name:  "no guest info at all yields NO coordinate, not a guess",
			guest: nil,
			want:  "",
		},
		{
			name:  "empty guest info yields no coordinate",
			guest: &vimtypes.GuestInfo{},
			want:  "",
		},
		{
			name:  "whitespace is not a coordinate",
			guest: &vimtypes.GuestInfo{HostName: "   ", IpAddress: "  "},
			want:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reachCoordinate(mo.VirtualMachine{Guest: tc.guest})
			if got != tc.want {
				t.Errorf("reachCoordinate = %q, want %q", got, tc.want)
			}
		})
	}
}

// The Facet must actually reach the projection, in the exact closed shape the mgmt.address
// schema demands ({address, port} — additionalProperties:false). A value the plugin computes
// and never emits is the defect class this ADR exists to close.
func TestNormalizeVMProjectsMgmtAddress(t *testing.T) {
	vm := mo.VirtualMachine{
		Config: &vimtypes.VirtualMachineConfigInfo{Uuid: "uuid-1", Hardware: vimtypes.VirtualHardware{NumCPU: 2, MemoryMB: 4096}},
		Guest:  &vimtypes.GuestInfo{HostName: "web-01.dev.stratt.test", IpAddress: "10.30.1.7"},
	}
	got, err := normalizeVM(vm)
	if err != nil {
		t.Fatalf("normalizeVM: %v", err)
	}
	raw, ok := got.Facets["mgmt.address"]
	if !ok {
		t.Fatal("mgmt.address must be projected — without it a provisioned VM cannot be targeted at all")
	}
	var facet map[string]any
	if err := json.Unmarshal(raw, &facet); err != nil {
		t.Fatalf("mgmt.address is not valid JSON: %v", err)
	}
	if facet["address"] != "web-01.dev.stratt.test" {
		t.Errorf("address = %v, want the guest FQDN", facet["address"])
	}
	// The schema is CLOSED (§9 — a reachability seam must never grow into a device
	// ontology). Emitting anything else would fail validation at the host.
	for k := range facet {
		if k != "address" && k != "port" {
			t.Errorf("mgmt.address carries %q; the schema is closed to {address, port}", k)
		}
	}
	// net.guest keeps the IP as the FACT it is — the two are not alternatives.
	if _, ok := got.Facets["net.guest"]; !ok {
		t.Error("net.guest must still carry the observed address for diagnosis")
	}
}

// A VM with no reachable coordinate must project everything else and simply omit the Facet.
func TestNormalizeVMOmitsMgmtAddressWhenUnknown(t *testing.T) {
	vm := mo.VirtualMachine{
		Config: &vimtypes.VirtualMachineConfigInfo{Uuid: "uuid-2"},
	}
	got, err := normalizeVM(vm)
	if err != nil {
		t.Fatalf("normalizeVM: %v", err)
	}
	if _, ok := got.Facets["mgmt.address"]; ok {
		t.Error("a VM with no guest info must have NO mgmt.address — 'built, not yet reachable' is a real state")
	}
	if _, ok := got.Facets["vm.config"]; !ok {
		t.Error("the rest of the projection must be unaffected")
	}
}
