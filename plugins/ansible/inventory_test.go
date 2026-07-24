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

// TestBuildInventoryRendersPort pins the second half of the closed mgmt.address
// coordinate (ADR-0084 {address, port?}): a declared port becomes ansible_port, an
// undeclared one renders NOTHING (ansible's own default applies — the shim never
// substitutes 22), and a port never rides a control-node connection.
func TestBuildInventoryRendersPort(t *testing.T) {
	inv := buildInventory([]Target{
		{Name: "custom", Address: "10.0.0.7", Port: 2222},
		{Name: "default", Address: "10.0.0.8"},
		{Name: "loopback", Address: "local", Port: 2222},
		{Name: "unrouted", Port: 2222},
	})
	lines := map[string]string{}
	for _, ln := range strings.Split(strings.TrimSpace(inv), "\n") {
		if f := strings.Fields(ln); len(f) > 0 && f[0] != "[all]" {
			lines[f[0]] = ln
		}
	}
	if got := lines["custom"]; !strings.Contains(got, "ansible_host=10.0.0.7") || !strings.Contains(got, "ansible_port=2222") {
		t.Fatalf("declared port must render ansible_port alongside ansible_host: %q", got)
	}
	if got := lines["default"]; strings.Contains(got, "ansible_port") {
		t.Fatalf("undeclared port must render NO ansible_port (ansible's default, never a shim-invented 22): %q", got)
	}
	if got := lines["loopback"]; strings.Contains(got, "ansible_port") {
		t.Fatalf("a control-node connection has no network port: %q", got)
	}
	if got := lines["unrouted"]; strings.Contains(got, "ansible_port") {
		t.Fatalf("a port with no address reaches nothing and must render nothing: %q", got)
	}
}

// TestBuildInventoryIsByteStable: the same resolved target set always renders the
// SAME inventory. Vars is a Go map, so unsorted iteration would make the file an
// operator reads during descent differ run to run for no reason (§1.8).
func TestBuildInventoryIsByteStable(t *testing.T) {
	tgt := []Target{{Name: "h1", Address: "10.0.0.1", Vars: map[string]string{
		"a_var": "1", "z_var": "26", "m_var": "13", "b_var": "2", "y_var": "25",
	}}}
	first := buildInventory(tgt)
	for range 50 {
		if got := buildInventory(tgt); got != first {
			t.Fatalf("inventory is not byte-stable across renders:\n %q\nvs %q", got, first)
		}
	}
	if !strings.Contains(first, "a_var=1 b_var=2 m_var=13 y_var=25 z_var=26") {
		t.Fatalf("vars must render in sorted key order: %q", first)
	}
}
