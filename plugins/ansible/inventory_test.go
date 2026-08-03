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

// ── ADR-0161 · the inventory renders GROUPS ──────────────────────────────────────────────
//
// Before this, buildInventory wrote `[all]`, the hosts, and `[all:vars]` — the whole file. So a
// migrated play saying `hosts: webservers` matched NOTHING, and `group_vars/webservers.yml` (which
// ADR-0134 D2 declares part of an Actuator's project) was never loaded, because ansible resolves
// group_vars by group name against the inventory's groups and there were none.
func TestInventoryRendersGroupSections(t *testing.T) {
	inv := buildInventory([]Target{
		{Name: "web-01", Address: "10.0.0.1", Groups: []string{"region_eu", "web"}},
		{Name: "web-02", Address: "10.0.0.2", Groups: []string{"region_eu", "web"}},
		{Name: "db-01", Address: "10.0.0.3", Groups: []string{"db", "region_us"}},
	})
	for _, want := range []string{
		"[all]\n",
		"web-01 ansible_host=10.0.0.1",
		"\n[web]\nweb-01\nweb-02\n",
		"\n[db]\ndb-01\n",
		"\n[region_eu]\nweb-01\nweb-02\n",
		"\n[region_us]\ndb-01\n",
	} {
		if !strings.Contains(inv, want) {
			t.Errorf("missing %q in:\n%s", want, inv)
		}
	}
	// THE HOST DEFINITION APPEARS ONCE. A host repeated with its vars in every section it belongs to
	// would define the same host several times, and ansible's precedence between those definitions
	// is a rule nobody should have to know.
	if strings.Count(inv, "ansible_host=10.0.0.1") != 1 {
		t.Errorf("a host must be DEFINED once and referenced by name:\n%s", inv)
	}
}

// Byte-stability (§1.8): two Runs over one target set must produce identical inventories or they
// cannot be compared during descent. Map iteration would break this on its own.
func TestGroupRenderingIsByteStable(t *testing.T) {
	ts := []Target{
		{Name: "b", Address: "10.0.0.2", Groups: []string{"z", "a"}},
		{Name: "a", Address: "10.0.0.1", Groups: []string{"a", "z"}},
	}
	first := buildInventory(ts)
	for range 20 {
		if buildInventory(ts) != first {
			t.Fatalf("inventory is not byte-stable across renders:\n%s", first)
		}
	}
	if !strings.Contains(first, "\n[a]\na\nb\n") || !strings.Contains(first, "\n[z]\na\nb\n") {
		t.Errorf("groups and members must both be sorted:\n%s", first)
	}
}

// A Run with no groupBy renders exactly what it rendered before ADR-0161 — the regression that
// matters most, because every shipped estate is this case.
func TestNoGroupsRendersTheOldInventoryExactly(t *testing.T) {
	inv := buildInventory([]Target{{Name: "h", Address: "10.0.0.1"}})
	if inv != "[all]\nh ansible_host=10.0.0.1\n" {
		t.Fatalf("an ungrouped Run must render byte-identically to before:\n%q", inv)
	}
}

// GROUPS NEVER WIDEN THE RUN (D3). Every name in a section is a host already in [all], because
// membership was resolved from the very targets the View selected. There is no path from a group
// name back into the graph — if there were, the play's `hosts:` line would become the authorization
// unit, which is content deciding blast radius (ADR-0028 refuses exactly that).
func TestAGroupOnlyEverContainsHostsAlreadyInAll(t *testing.T) {
	inv := buildInventory([]Target{
		{Name: "only-host", Address: "10.0.0.1", Groups: []string{"web"}},
	})
	all, groups := map[string]bool{}, map[string][]string{}
	section := ""
	for _, line := range strings.Split(inv, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "["):
			section = strings.Trim(line, "[]")
		case section == "all":
			all[strings.Fields(line)[0]] = true
		default:
			groups[section] = append(groups[section], line)
		}
	}
	for g, hosts := range groups {
		for _, h := range hosts {
			if !all[h] {
				t.Errorf("group %q references %q, which is not in [all] — a group must be a PARTITION "+
					"of the blast radius, never a way to reach a host the View did not select", g, h)
			}
		}
	}
}
