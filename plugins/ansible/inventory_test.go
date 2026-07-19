package ansible

import (
	"strings"
	"testing"
)

// TestBuildInventoryAddressMapping pins ADR-0084: the SHIM renders the core's
// typed Address into ansible's connection var — the core never authors ansible_host.
// A real address → ansible_host; the reserved "local" → ansible_connection=local;
// an empty Address → no connection var (unreachable fails loudly, never silent-local).
func TestBuildInventoryAddressMapping(t *testing.T) {
	inv := buildInventory([]Target{
		{Name: "real", Address: "10.0.0.7"},
		{Name: "loopback", Address: "local"},
		{Name: "unrouted"},
		{Name: "withvars", Address: "10.0.0.8", Vars: map[string]string{"ansible_user": "deploy"}},
	})
	lines := map[string]string{}
	for _, ln := range strings.Split(strings.TrimSpace(inv), "\n") {
		if f := strings.Fields(ln); len(f) > 0 && f[0] != "[all]" {
			lines[f[0]] = ln
		}
	}
	if got := lines["real"]; !strings.Contains(got, "ansible_host=10.0.0.7") || strings.Contains(got, "ansible_connection") {
		t.Fatalf("real address must render ansible_host, no connection override: %q", got)
	}
	if got := lines["loopback"]; !strings.Contains(got, "ansible_connection=local") || strings.Contains(got, "ansible_host") {
		t.Fatalf("reserved 'local' must render ansible_connection=local, never ansible_host: %q", got)
	}
	if got := lines["unrouted"]; strings.Contains(got, "ansible_host") || strings.Contains(got, "ansible_connection") {
		t.Fatalf("no address must emit NO connection var (loud fail, not silent local): %q", got)
	}
	if got := lines["withvars"]; !strings.Contains(got, "ansible_host=10.0.0.8") || !strings.Contains(got, "ansible_user=deploy") {
		t.Fatalf("typed address and genuine tool vars must coexist: %q", got)
	}
	// The core never authored an ansible_host key anywhere in Vars; every ansible_host
	// present was rendered by the shim FROM Address.
	if strings.Count(inv, "ansible_host") != 2 {
		t.Fatalf("exactly the two real-address hosts get ansible_host: %q", inv)
	}
}
