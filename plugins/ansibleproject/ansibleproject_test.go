package ansibleproject

import (
	"encoding/json"
	"testing"
	"testing/fstest"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// fakeRoot is an in-memory Ansible content root — playbooks, roles, a Galaxy
// requirements.yml, and inventory files — enough to exercise the Syncer's parse +
// projection with no real disk (the plugin's content-expertise tested in isolation —
// no gRPC, no core).
func fakeRoot() fstest.MapFS {
	return fstest.MapFS{
		// A real playbook: a sequence of plays, each with `hosts:`.
		"site.yml": &fstest.MapFile{Data: []byte(`
- hosts: web
  become: true
  roles:
    - nginx
- hosts: db
  tasks:
    - ping:
`)},
		// A wrapper playbook: only import_playbook entries (no hosts) — still a playbook.
		"playbooks/deploy.yml": &fstest.MapFile{Data: []byte("- import_playbook: ../site.yml\n")},
		// Two roles (directories under roles/). Their task files are sequences too, but
		// carry no `hosts:` — they must NOT be mistaken for playbooks.
		"roles/nginx/tasks/main.yml":  &fstest.MapFile{Data: []byte("- name: install\n  apt:\n    name: nginx\n")},
		"roles/nginx/meta/main.yml":   &fstest.MapFile{Data: []byte("galaxy_info:\n  author: x\n")},
		"roles/common/tasks/main.yml": &fstest.MapFile{Data: []byte("- name: motd\n  copy:\n    dest: /etc/motd\n    content: hi\n")},
		// Galaxy collections: a bare FQCN string form and a {name,version,source} map form.
		"requirements.yml": &fstest.MapFile{Data: []byte(`
collections:
  - community.general
  - name: ansible.posix
    version: 1.5.4
    source: https://galaxy.ansible.com
`)},
		// Inventories: an INI hosts file and a YAML inventory, both under inventory/.
		"inventory/hosts":    &fstest.MapFile{Data: []byte("[web]\nweb1\n")},
		"inventory/prod.yml": &fstest.MapFile{Data: []byte("all:\n  hosts:\n    web1:\n")},
		// A non-playbook YAML (a mapping) must be projected as nothing.
		"group_vars/all.yml": &fstest.MapFile{Data: []byte("nginx_port: 80\n")},
	}
}

func TestEnumerateAndNormalize(t *testing.T) {
	c := New(Config{FS: fakeRoot(), ProjectID: "webproj"})

	snap, err := c.Enumerate()
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	if len(snap.Playbooks) != 2 || len(snap.Roles) != 2 || len(snap.Collections) != 2 || len(snap.Inventories) != 2 {
		t.Fatalf("snapshot counts wrong: playbooks=%d roles=%d collections=%d inventories=%d",
			len(snap.Playbooks), len(snap.Roles), len(snap.Collections), len(snap.Inventories))
	}

	ents, err := c.Normalize(snap)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	byKind := map[string][]*pluginv1.ObservedEntity{}
	for _, e := range ents {
		byKind[e.GetKind()] = append(byKind[e.GetKind()], e)
	}
	for _, k := range []string{KindPlaybook, KindRole, KindCollection, KindInventory} {
		if len(byKind[k]) == 0 {
			t.Fatalf("missing projected kind %q", k)
		}
	}

	// Identity is project-qualified so two content roots never collide.
	var site *pluginv1.ObservedEntity
	for _, e := range byKind[KindPlaybook] {
		if e.GetIdentityKeys()[KindPlaybook] == "webproj/site.yml" {
			site = e
		}
	}
	if site == nil {
		t.Fatalf("site.yml playbook not projected with qualified identity; got %v", ids(byKind[KindPlaybook], KindPlaybook))
	}

	// The playbook facet carries observed METADATA (the plays + host patterns) — never a
	// reinterpretation of Ansible's execution model.
	var pf struct {
		Name  string   `json:"name"`
		Plays int      `json:"plays"`
		Hosts []string `json:"hosts"`
	}
	if err := json.Unmarshal(site.GetFacets()[KindPlaybook], &pf); err != nil {
		t.Fatalf("playbook facet decode: %v", err)
	}
	if pf.Name != "site.yml" || pf.Plays != 2 || len(pf.Hosts) != 2 {
		t.Fatalf("playbook facet wrong: %+v", pf)
	}

	// The {name,version,source} collection form is preserved.
	var posix *struct {
		Name, Version, Source string
	}
	for _, e := range byKind[KindCollection] {
		var cf struct{ Name, Version, Source string }
		_ = json.Unmarshal(e.GetFacets()[KindCollection], &cf)
		if cf.Name == "ansible.posix" {
			posix = &cf
		}
	}
	if posix == nil || posix.Version != "1.5.4" {
		t.Fatalf("ansible.posix collection version not preserved: %+v", posix)
	}

	// Inventory format is classified (ini vs yaml).
	fmts := map[string]string{}
	for _, e := range byKind[KindInventory] {
		var inv struct{ Path, Format string }
		_ = json.Unmarshal(e.GetFacets()[KindInventory], &inv)
		fmts[inv.Path] = inv.Format
	}
	if fmts["inventory/hosts"] != "ini" || fmts["inventory/prod.yml"] != "yaml" {
		t.Fatalf("inventory formats wrong: %+v", fmts)
	}

	// Every projected entity carries the project label so a View can group by project.
	for _, e := range ents {
		if e.GetLabels()["ansible.project"] != "webproj" {
			t.Fatalf("entity %q missing ansible.project label: %v", e.GetKind(), e.GetLabels())
		}
	}
}

func TestEmptyReadIsNotAFullSyncByDefault(t *testing.T) {
	// A missing/unmounted content root reads empty; it must NOT assert a full sync (which
	// would tombstone the whole mirror) unless explicitly allowed (§1.8 guardrail).
	if (ServerConfig{}).AllowEmptyFullSync {
		t.Fatal("AllowEmptyFullSync must default false")
	}
	// An empty tree yields zero entities (the guardrail then holds at the server).
	c := New(Config{FS: fstest.MapFS{}, ProjectID: "empty"})
	snap, err := c.Enumerate()
	if err != nil {
		t.Fatalf("enumerate empty: %v", err)
	}
	ents, err := c.Normalize(snap)
	if err != nil {
		t.Fatalf("normalize empty: %v", err)
	}
	if len(ents) != 0 {
		t.Fatalf("empty root must project nothing, got %d", len(ents))
	}
}

func ids(es []*pluginv1.ObservedEntity, scheme string) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.GetIdentityKeys()[scheme])
	}
	return out
}
