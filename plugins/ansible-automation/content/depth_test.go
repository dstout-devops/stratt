package content

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
)

// A content root exercising every shape this batch added: an in-tree role with meta
// dependencies and galaxy_info, a requirements file declaring BOTH halves, group_vars and
// host_vars in file and directory form, and a vaulted scope.
func depthFS() fstest.MapFS {
	return fstest.MapFS{
		"site.yml": {Data: []byte("- hosts: all\n  roles: [web]\n")},

		"roles/web/tasks/main.yml": {Data: []byte("- name: x\n  ansible.builtin.debug:\n")},
		"roles/web/meta/main.yml": {Data: []byte(`
galaxy_info:
  author: platform-team
  license: Apache-2.0
  min_ansible_version: "2.15"
  platforms:
    - name: EL
      versions: [9]
    - name: Debian
dependencies:
  - common
  - role: geerlingguy.apache
  - { name: monitoring }
`)},
		"roles/common/tasks/main.yml": {Data: []byte("- name: y\n  ansible.builtin.debug:\n")},

		// BOTH halves of requirements.yml (ANS-002): only `collections:` was parsed before.
		"requirements.yml": {Data: []byte(`
collections:
  - name: community.general
    version: "13.2.0"
roles:
  - name: geerlingguy.apache
    version: "5.0.0"
    src: https://github.com/geerlingguy/ansible-role-apache
  - monitoring
`)},

		// group_vars: file form, directory form, and a vaulted file (ANS-003/008).
		"group_vars/all.yml":       {Data: []byte("ntp_server: pool.ntp.org\ntimezone: UTC\n")},
		"group_vars/web/vars.yml":  {Data: []byte("http_port: 8080\n")},
		"group_vars/web/ports.yml": {Data: []byte("https_port: 8443\n")},
		"group_vars/secrets.yml": {Data: []byte(
			"$ANSIBLE_VAULT;1.1;AES256\n39383166643764316...\n")},
		"host_vars/web-01.yml": {Data: []byte("http_port: 9090\nadmin_password: hunter2\n")},
	}
}

// testClient roots a client at an in-memory tree, with a fixed project id so identity
// values in assertions are stable.
func testClient(t *testing.T, fsys fstest.MapFS) *Client {
	t.Helper()
	return New(Config{FS: fsys, ProjectID: "webproj"})
}

func facetsByKind(t *testing.T, c *Client, snap *Snapshot) map[string][]map[string]any {
	t.Helper()
	ents, err := c.Normalize(snap)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]map[string]any{}
	for _, e := range ents {
		var f map[string]any
		if err := json.Unmarshal(e.GetFacets()[e.GetKind()], &f); err != nil {
			t.Fatal(err)
		}
		out[e.GetKind()] = append(out[e.GetKind()], f)
	}
	return out
}

// ── ANS-003 + ANS-008 ────────────────────────────────────────────────────────────────────

// THE §2.5 PROPERTY: scope and KEY NAMES reach the graph; values never do. `admin_password`
// is in the fixture on purpose — a group_vars file routinely holds credentials in the clear,
// which is the whole reason people vault them.
func TestVarScopesProjectNamesAndNeverValues(t *testing.T) {
	c := testClient(t, depthFS())
	snap, err := c.Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	ents, err := c.Normalize(snap)
	if err != nil {
		t.Fatal(err)
	}
	var blob strings.Builder
	for _, e := range ents {
		for _, doc := range e.GetFacets() {
			blob.Write(doc)
		}
	}
	for _, value := range []string{"hunter2", "pool.ntp.org", "8080", "8443", "9090", "UTC"} {
		if strings.Contains(blob.String(), value) {
			t.Errorf("a variable VALUE (%q) reached the graph — a vars file routinely holds "+
				"credentials in the clear (§2.5): %s", value, blob.String())
		}
	}
	for _, name := range []string{"ntp_server", "timezone", "http_port", "https_port", "admin_password"} {
		if !strings.Contains(blob.String(), name) {
			t.Errorf("the variable NAME %q is missing — names are what answer 'why did this host "+
				"get this value', and they are not secret: %s", name, blob.String())
		}
	}
}

func TestVarScopeShapes(t *testing.T) {
	c := testClient(t, depthFS())
	snap, err := c.Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]VarScope{}
	for _, vs := range snap.VarScopes {
		byPath[vs.Path] = vs
	}
	if len(byPath) != 4 {
		t.Fatalf("want 4 scopes (all.yml, web/, secrets.yml, web-01.yml), got %d: %v", len(byPath), snap.VarScopes)
	}

	// The FILE form.
	if got := byPath["group_vars/all.yml"]; got.Scope != "group" || got.Target != "all" ||
		strings.Join(got.Keys, ",") != "ntp_server,timezone" {
		t.Errorf("group_vars/all.yml = %+v", got)
	}
	// The DIRECTORY form unions its files — ansible treats it as one scope, and splitting it
	// per file would invent a distinction the estate does not have.
	if got := byPath["group_vars/web"]; got.Target != "web" ||
		strings.Join(got.Keys, ",") != "http_port,https_port" {
		t.Errorf("group_vars/web/ must union its files: %+v", got)
	}
	if got := byPath["host_vars/web-01.yml"]; got.Scope != "host" || got.Target != "web-01" {
		t.Errorf("host_vars/web-01.yml = %+v", got)
	}
}

// ANS-008. Present AND vaulted, with no keys and no decryption. "Binds nothing" and "binds
// things I cannot show you" are different answers and must not render the same (§1.8).
func TestVaultedScopeIsPresentAndSaysSo(t *testing.T) {
	c := testClient(t, depthFS())
	snap, err := c.Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	for _, vs := range snap.VarScopes {
		if vs.Path != "group_vars/secrets.yml" {
			continue
		}
		if !vs.Vaulted {
			t.Fatal("a $ANSIBLE_VAULT file must be reported as vaulted")
		}
		if len(vs.Keys) != 0 {
			t.Fatalf("a vaulted file must yield NO keys — reading them means decrypting it, and "+
				"this plugin holds no vault password and must not want one (§2.5): %v", vs.Keys)
		}
		return
	}
	t.Fatal("the vaulted scope was dropped entirely — present-and-vaulted is the fact worth having")
}

// A vars file can hold a Jinja template that is not valid YAML until rendered. Failing the
// whole Observe over one would take every other artifact down with it.
func TestUnparseableVarsFileIsNotFatal(t *testing.T) {
	c := testClient(t, fstest.MapFS{
		"group_vars/all.yml": {Data: []byte("{{ not: valid: yaml until rendered\n")},
		"site.yml":           {Data: []byte("- hosts: all\n")},
	})
	snap, err := c.Enumerate()
	if err != nil {
		t.Fatalf("a templated vars file must not fail the sync: %v", err)
	}
	if len(snap.VarScopes) != 1 || len(snap.VarScopes[0].Keys) != 0 {
		t.Errorf("it should still be observed as PRESENT with no readable keys: %+v", snap.VarScopes)
	}
}

// ── ANS-004 · role meta ──────────────────────────────────────────────────────────────────

func TestRoleMetaYieldsDependenciesAndProvenance(t *testing.T) {
	c := testClient(t, depthFS())
	snap, err := c.Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	var web *Role
	for i := range snap.Roles {
		if snap.Roles[i].Name == "web" {
			web = &snap.Roles[i]
		}
	}
	if web == nil {
		t.Fatal("role web missing")
	}
	// All THREE Galaxy dependency forms: bare string, {role: …}, {name: …}. Accepting only
	// the mapping would silently drop the dependencies of every role written the short way.
	if strings.Join(web.Dependencies, ",") != "common,geerlingguy.apache,monitoring" {
		t.Errorf("dependencies = %v", web.Dependencies)
	}
	if web.Author != "platform-team" || web.License != "Apache-2.0" || web.MinAnsibleVersion != "2.15" {
		t.Errorf("galaxy_info not projected: %+v", web)
	}
	if strings.Join(web.Platforms, ",") != "EL,Debian" {
		t.Errorf("platforms = %v", web.Platforms)
	}
}

// THE EDGE MUST POINT AT THE ROLE IT MEANS. meta/main.yml names a role, not a location, and
// that name may be an in-tree role or a Galaxy one — so the target cannot be computed from
// the name alone. The first version always pointed into the requirements space, which left
// every dependency on an IN-TREE role dangling forever.
func TestDependencyEdgeResolvesInTreeAndExternalDifferently(t *testing.T) {
	c := testClient(t, depthFS())
	snap, err := c.Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	ents, err := c.Normalize(snap)
	if err != nil {
		t.Fatal(err)
	}
	var targets []string
	for _, e := range ents {
		for _, r := range e.GetRelations() {
			if r.GetType() == "depends-on" {
				targets = append(targets, r.GetToValue())
			}
		}
	}
	want := map[string]bool{
		c.qualify("roles/common"):                    false, // in-tree → its PATH
		c.qualify("requirements/geerlingguy.apache"): false, // declared in requirements.yml
		c.qualify("requirements/monitoring"):         false, // declared there too
	}
	for _, got := range targets {
		if _, ok := want[got]; !ok {
			t.Errorf("unexpected dependency target %q", got)
			continue
		}
		want[got] = true
	}
	for target, seen := range want {
		if !seen {
			t.Errorf("dependency edge to %q missing — a dependency on an in-tree role that "+
				"resolved into the requirements space would dangle forever", target)
		}
	}
}

// ── ANS-002 · the roles half of requirements.yml ─────────────────────────────────────────

func TestRequirementsRolesJoinTheSameKind(t *testing.T) {
	c := testClient(t, depthFS())
	snap, err := c.Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Role{}
	for _, r := range snap.Roles {
		byName[r.Name] = r
	}
	if len(byName) != 4 {
		t.Fatalf("want web, common (in-tree) + geerlingguy.apache, monitoring (required), got %v", byName)
	}
	if got := byName["geerlingguy.apache"]; !got.Required || got.Version != "5.0.0" || got.Path != "" {
		t.Errorf("a required role carries its version and NO path: %+v", got)
	}
	// The bare-string form must work too — real requirements files use both.
	if got := byName["monitoring"]; !got.Required {
		t.Errorf("the bare-string roles form was dropped: %+v", got)
	}
	if got := byName["web"]; got.Required || got.Path != "roles/web" {
		t.Errorf("an in-tree role is not required and carries its path: %+v", got)
	}

	// Collections must still parse — the two halves live in one file and one read.
	if len(snap.Collections) != 1 || snap.Collections[0].Name != "community.general" {
		t.Errorf("the collections half regressed: %v", snap.Collections)
	}
}

// THE IDENTITY COLLISION, pinned. `roleID` first used "roles/"+name for the required space,
// which is byte-identical to an in-tree role's path: an in-tree `apache` and a requirements
// entry named `apache` produced ONE identity, so one silently overwrote the other — the same
// entity asserted twice with different facets, and no error anywhere.
func TestInTreeAndRequiredRoleOfTheSameNameDoNotCollide(t *testing.T) {
	c := testClient(t, fstest.MapFS{
		"roles/apache/tasks/main.yml": {Data: []byte("- name: x\n  ansible.builtin.debug:\n")},
		"requirements.yml":            {Data: []byte("roles:\n  - apache\n")},
		"site.yml":                    {Data: []byte("- hosts: all\n")},
	})
	snap, err := c.Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	facets := facetsByKind(t, c, snap)
	if len(facets[KindRole]) != 2 {
		t.Fatalf("want 2 role entities, got %d — a collision silently drops one", len(facets[KindRole]))
	}
	ents, err := c.Normalize(snap)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range ents {
		if e.GetKind() != KindRole {
			continue
		}
		id := e.GetIdentityKeys()[KindRole]
		if seen[id] {
			t.Fatalf("two role entities share identity %q", id)
		}
		seen[id] = true
	}
}

// A root with none of this must project exactly as it did before — the batch adds facts, it
// does not change what an existing content root means.
func TestBareRootIsUnchanged(t *testing.T) {
	c := testClient(t, fstest.MapFS{"site.yml": {Data: []byte("- hosts: all\n")}})
	snap, err := c.Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.VarScopes) != 0 || len(snap.Roles) != 0 {
		t.Errorf("a bare root gained artifacts: %+v", snap)
	}
	facets := facetsByKind(t, c, snap)
	if len(facets[KindPlaybook]) != 1 {
		t.Errorf("the playbook projection changed: %v", facets)
	}
}
