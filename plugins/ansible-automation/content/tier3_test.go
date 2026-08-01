package content

import (
	"strings"
	"testing"
	"testing/fstest"
)

// ── ANS-005 · ansible.cfg ────────────────────────────────────────────────────────────────

// THE FIX, not the observation. A root that configures its roles elsewhere previously
// projected ZERO roles and reported no problem — the projection was reading a layout the
// tool was not using, and nothing anywhere said so.
func TestRolesPathIsHonoredAndNotJustRecorded(t *testing.T) {
	fsys := fstest.MapFS{
		"ansible.cfg": {Data: []byte("[defaults]\nroles_path = galaxy_roles:roles\n")},
		"galaxy_roles/geerlingguy.nginx/tasks/main.yml": {Data: []byte("- name: x\n  ansible.builtin.debug:\n")},
		"roles/local/tasks/main.yml":                    {Data: []byte("- name: y\n  ansible.builtin.debug:\n")},
		"site.yml":                                      {Data: []byte("- hosts: all\n")},
	}
	snap, err := testClient(t, fsys).Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, r := range snap.Roles {
		names[r.Name] = r.Path
	}
	if len(names) != 2 {
		t.Fatalf("both search paths must be read, got %v", names)
	}
	if names["geerlingguy.nginx"] != "galaxy_roles/geerlingguy.nginx" {
		t.Errorf("a role under the configured roles_path was missed: %v", names)
	}
	if names["local"] != "roles/local" {
		t.Errorf("roles/ must still be read whether or not the config mentions it: %v", names)
	}
}

// A path outside the root cannot be read and must not be pretended to have been read. It
// still shows up in the projected rolesPath, so the gap is visible rather than silent.
func TestRolesSearchPathsSkipWhatItCannotRead(t *testing.T) {
	cfg := parseINI([]byte("[defaults]\nroles_path = /etc/ansible/roles:~/.ansible/roles:../shared:vendor_roles\n"))
	got := strings.Join(rolesSearchPaths(cfg), ",")
	if got != "roles,vendor_roles" {
		t.Fatalf("search paths = %q — absolute, home-relative and escaping entries name locations "+
			"this Syncer cannot read", got)
	}
	if !strings.Contains(cfg.Settings["rolesPath"], "/etc/ansible/roles") {
		t.Error("…but the configured value must still be projected, so an operator can SEE that " +
			"content lives somewhere this projection does not cover (§1.8)")
	}
	if rolesSearchPaths(nil)[0] != "roles" || len(rolesSearchPaths(nil)) != 1 {
		t.Error("no config means the default search path and nothing else")
	}
}

// THE §2.5 LINE for this file, and it is not theoretical: a [galaxy_server.*] section takes
// a real Galaxy API token. An allowlist keeps it out BY CONSTRUCTION — a key nobody added
// contributes its name and nothing else — where a denylist would have to anticipate it.
func TestConfigProjectsAllowlistedValuesAndOnlyNamesForTheRest(t *testing.T) {
	cfg := parseINI([]byte(`
# a comment
[defaults]
roles_path = galaxy_roles
host_key_checking = False
forks = 25
stdout_callback = yaml
vault_password_file = ~/.vault_pass   ; inline comment

[galaxy_server.private]
url = https://hub.example.com/
token = 3f7c9a2b-SECRET-TOKEN

[ssh_connection]
pipelining = True
`))
	if cfg.Settings["rolesPath"] != "galaxy_roles" || cfg.Settings["hostKeyChecking"] != "False" ||
		cfg.Settings["forks"] != "25" || cfg.Settings["stdoutCallback"] != "yaml" {
		t.Fatalf("allowlisted settings must carry their values: %v", cfg.Settings)
	}
	// A path is not material, and the inline `;` comment must not become part of it.
	if cfg.Settings["vaultPasswordFile"] != "~/.vault_pass" {
		t.Errorf("vaultPasswordFile = %q", cfg.Settings["vaultPasswordFile"])
	}
	for _, s := range cfg.Settings {
		if strings.Contains(s, "SECRET-TOKEN") {
			t.Fatalf("a Galaxy API token reached the projected settings: %v", cfg.Settings)
		}
	}
	other := strings.Join(cfg.OtherKeys, ",")
	if strings.Contains(other, "SECRET-TOKEN") || strings.Contains(other, "hub.example.com") {
		t.Fatalf("otherKeys must carry NAMES only: %q", other)
	}
	if other != "galaxy_server.private.token,galaxy_server.private.url,ssh_connection.pipelining" {
		t.Errorf("otherKeys = %q — section-qualified, sorted, names only", other)
	}
}

func TestConfigIsOptional(t *testing.T) {
	snap, err := testClient(t, fstest.MapFS{"site.yml": {Data: []byte("- hosts: all\n")}}).Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Config != nil {
		t.Errorf("a root with no ansible.cfg has none: %+v", snap.Config)
	}
}

// ── ANS-006 · the repo's own code ────────────────────────────────────────────────────────

// Both layouts, because a collection-shaped repo puts its modules under plugins/ and a
// playbook repo puts them under library/. Covering one would report half of a real repo as
// shipping no custom content — the exact class of silent gap this batch is closing.
func TestCustomPluginsAreFoundInBothLayouts(t *testing.T) {
	fsys := fstest.MapFS{
		"site.yml":                            {Data: []byte("- hosts: all\n")},
		"library/acme_thing.py":               {Data: []byte("# module\n")},
		"filter_plugins/acme_filters.py":      {Data: []byte("# filter\n")},
		"plugins/modules/collection_thing.py": {Data: []byte("# module\n")},
		"plugins/lookup/acme_lookup.py":       {Data: []byte("# lookup\n")},
		"roles/web/library/role_local.py":     {Data: []byte("# module\n")},
		"library/__init__.py":                 {Data: []byte("")},
		"library/acme_thing.pyc":              {Data: []byte("")},
		"roles/web/tasks/main.yml":            {Data: []byte("- name: x\n  ansible.builtin.debug:\n")},
	}
	snap, err := testClient(t, fsys).Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Plugin{}
	for _, p := range snap.Plugins {
		got[p.Path] = p
	}
	if len(got) != 5 {
		t.Fatalf("want 5 plugins, got %d: %v", len(got), got)
	}
	for path, typ := range map[string]string{
		"library/acme_thing.py":               "module",
		"filter_plugins/acme_filters.py":      "filter",
		"plugins/modules/collection_thing.py": "module",
		"plugins/lookup/acme_lookup.py":       "lookup",
		"roles/web/library/role_local.py":     "module",
	} {
		if got[path].Type != typ {
			t.Errorf("%s: type = %q, want %q", path, got[path].Type, typ)
		}
	}
	// A role-local module travels WITH the role and is a different migration risk from a
	// repo-wide one, so which role owns it is part of the fact.
	if got["roles/web/library/role_local.py"].Role != "web" {
		t.Errorf("a role-local plugin must name its role: %+v", got["roles/web/library/role_local.py"])
	}
	if got["library/acme_thing.py"].Role != "" {
		t.Error("a repo-wide plugin belongs to no role")
	}
	// Packaging, not content an operator migrates.
	for _, skipped := range []string{"library/__init__.py", "library/acme_thing.pyc"} {
		if _, present := got[skipped]; present {
			t.Errorf("%s is packaging, not custom content", skipped)
		}
	}
}

// A task file inside a role is not a plugin, and neither is a playbook. Matching on the
// DIRECTORY is what keeps that true without reading any of them.
func TestOrdinaryContentIsNotMistakenForAPlugin(t *testing.T) {
	snap, err := testClient(t, depthFS()).Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Plugins) != 0 {
		t.Errorf("a root with no custom code must project none: %v", snap.Plugins)
	}
}

// ── ANS-007 · the root that IS a collection ──────────────────────────────────────────────

func TestGalaxyRootProjectsAsTheCollectionItIs(t *testing.T) {
	fsys := fstest.MapFS{
		"galaxy.yml": {Data: []byte(`
namespace: acme
name: platform
version: 2.1.0
description: The ACME platform collection
license: [Apache-2.0]
dependencies:
  community.general: ">=13.0.0"
  ansible.posix: "*"
`)},
		"requirements.yml":     {Data: []byte("collections:\n  - name: community.crypto\n    version: \"2.22.3\"\n")},
		"playbooks/deploy.yml": {Data: []byte("- hosts: all\n")},
	}
	c := testClient(t, fsys)
	snap, err := c.Enumerate()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Galaxy == nil || snap.Galaxy.FQCN() != "acme.platform" || snap.Galaxy.Version != "2.1.0" {
		t.Fatalf("galaxy.yml not read: %+v", snap.Galaxy)
	}
	byName := map[string]Collection{}
	for _, col := range snap.Collections {
		byName[col.Name] = col
	}
	if len(byName) != 2 {
		t.Fatalf("the root collection AND the required one, got %v", byName)
	}
	root := byName["acme.platform"]
	if !root.Root || root.Path != "galaxy.yml" {
		t.Errorf("the root collection must be marked and carry its manifest path: %+v", root)
	}
	// A collection's OWN dependencies live in galaxy.yml, not requirements.yml — a projection
	// reading only requirements.yml sees none of them.
	if root.Dependencies["community.general"] != ">=13.0.0" || len(root.Dependencies) != 2 {
		t.Errorf("galaxy.yml dependencies missing: %+v", root.Dependencies)
	}
	if byName["community.crypto"].Root {
		t.Error("a required collection is not the root")
	}

	// The two identity spaces must stay disjoint: a path cannot collide with a bare FQCN.
	ents, err := c.Normalize(snap)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, e := range ents {
		if e.GetKind() != KindCollection {
			continue
		}
		id := e.GetIdentityKeys()[KindCollection]
		if ids[id] {
			t.Fatalf("two collection entities share identity %q", id)
		}
		ids[id] = true
	}
	if !ids[c.qualify("galaxy.yml")] || !ids[c.qualify("community.crypto")] {
		t.Errorf("identities = %v", ids)
	}
}

// A galaxy.yml that is not a collection manifest — or does not parse — must not fail the
// whole Observe. The rest of the tree still projects (§1.8).
func TestUnusableGalaxyFileIsNotFatal(t *testing.T) {
	for name, doc := range map[string]string{
		"unparseable": "{{ not yaml\n",
		"no name":     "namespace: acme\nversion: 1.0.0\n",
	} {
		snap, err := testClient(t, fstest.MapFS{
			"galaxy.yml": {Data: []byte(doc)},
			"site.yml":   {Data: []byte("- hosts: all\n")},
		}).Enumerate()
		if err != nil {
			t.Errorf("%s: must not fail the sync: %v", name, err)
			continue
		}
		if snap.Galaxy != nil {
			t.Errorf("%s: must not be treated as a collection root: %+v", name, snap.Galaxy)
		}
		if len(snap.Playbooks) != 1 {
			t.Errorf("%s: the rest of the tree must still project", name)
		}
	}
}
