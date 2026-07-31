package content

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

// The METADATA this Syncer observes about each Ansible artifact. Per the charter
// non-goal (no new config language) the plugin projects only metadata — name, path,
// the hosts a play targets, the collections required — and NEVER reinterprets or
// re-emits playbook/role execution semantics. Parsing to observe is fine; normalizing
// Ansible's execution model into a Stratt dialect is the line not crossed.

// Playbook is a YAML file whose top level is a sequence of plays (mappings with a
// `hosts:` key, or `import_playbook:` wrappers) — the shape that distinguishes a
// playbook from a role task file (also a sequence, but of tasks with no `hosts`).
type Playbook struct {
	Path  string
	Plays int
	Hosts []string // the host patterns the plays target (observed, not resolved)
}

// Role is a role this project uses: a directory under roles/, OR a dependency declared in
// requirements.yml (ANS-002). ONE Kind for both, because "which roles does this project use, and
// which come from outside the repo" is one question — two Kinds would make it two queries and a
// join, and would leave a dependency EDGE unable to resolve without knowing which it pointed at.
type Role struct {
	Name string
	// Path is the in-tree location; EMPTY for a required role, which is the discriminator a
	// reader actually cares about alongside Required.
	Path string
	// Required marks a role declared in requirements.yml rather than present in roles/ —
	// content that must be fetched before this project can run (ANS-002).
	Required bool
	Version  string
	Source   string
	// Dependencies are the role names meta/main.yml declares (ANS-004), projected as
	// `depends-on` edges. The one graph inside a role, and the only thing read from within
	// one: tasks/handlers/defaults stay unread, which is the §9 line the audit holds.
	Dependencies []string
	// galaxy_info provenance — cheap metadata once meta/ is being read at all (ANS-004).
	Author            string
	License           string
	MinAnsibleVersion string
	Platforms         []string
}

// Collection is a Galaxy collection dependency declared in requirements.yml.
type Collection struct {
	Name    string // the FQCN, e.g. community.general
	Version string
	Source  string
}

// Inventory is an inventory file/source — the hosts+groups a run targets.
type Inventory struct {
	Path   string
	Format string // ini | yaml
}

// Snapshot is one full read of the content root's Ansible artifacts.
type Snapshot struct {
	Playbooks   []Playbook
	Roles       []Role
	Collections []Collection
	Inventories []Inventory
	// VarScopes are the group_vars/host_vars binding sites (ANS-003/008) — scope and KEY
	// NAMES, never values.
	VarScopes []VarScope
}

// Enumerate performs one full read of the content root. A parse failure on a
// requirements.yml fails the whole Observe (an empty projection is never presented as
// a successful full-sync — the empty-snapshot guardrail then holds steady, §1.8). A
// file that merely fails to look like a playbook is silently skipped (not an error).
func (c *Client) Enumerate() (*Snapshot, error) {
	var snap Snapshot
	var err error
	if snap.Roles, err = c.roles(); err != nil {
		return nil, err
	}
	// ONE read of requirements.yml yields BOTH halves (ANS-002). Only `collections:` was
	// parsed before, so a project's role dependencies were invisible while its collection
	// dependencies were not — an asymmetry inside a single file.
	var required []Role
	if snap.Collections, required, err = c.requirements(); err != nil {
		return nil, err
	}
	snap.Roles = append(snap.Roles, required...)
	if snap.Playbooks, snap.Inventories, err = c.content(); err != nil {
		return nil, err
	}
	if snap.VarScopes, err = c.varScopes(); err != nil {
		return nil, err
	}
	return &snap, nil
}

// roles reads the immediate subdirectories of roles/ (each a reusable role).
func (c *Client) roles() ([]Role, error) {
	var out []Role
	ents, err := fs.ReadDir(c.fsys, "roles")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ansible-automation content: read roles/: %w", err)
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		r := Role{Name: e.Name(), Path: "roles/" + e.Name()}
		if err := c.readRoleMeta(&r); err != nil {
			return nil, fmt.Errorf("ansible-automation content: read %s/meta: %w", r.Path, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// requirements parses BOTH halves of the standard requirements.yml locations: the Galaxy
// collection dependencies and (ANS-002) the role dependencies. Each entry of either is a bare
// name string or a mapping — both Galaxy-legal forms, and real repos use both.
func (c *Client) requirements() ([]Collection, []Role, error) {
	var out []Collection
	var roles []Role
	for _, p := range []string{"requirements.yml", "requirements.yaml", "collections/requirements.yml", "collections/requirements.yaml"} {
		b, err := fs.ReadFile(c.fsys, p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("ansible-automation content: read %s: %w", p, err)
		}
		var root yaml.Node
		if err := yaml.Unmarshal(b, &root); err != nil {
			return nil, nil, fmt.Errorf("ansible-automation content: parse %s: %w", p, err)
		}
		roles = append(roles, parseRequirementsRoles(&root)...)
		var doc struct {
			Collections []yaml.Node `yaml:"collections"`
		}
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return nil, nil, fmt.Errorf("ansible-automation content: parse %s: %w", p, err)
		}
		for _, n := range doc.Collections {
			if n.Kind == yaml.ScalarNode {
				out = append(out, Collection{Name: n.Value})
				continue
			}
			var m struct {
				Name    string `yaml:"name"`
				Version string `yaml:"version"`
				Source  string `yaml:"source"`
			}
			if err := n.Decode(&m); err == nil && m.Name != "" {
				out = append(out, Collection{Name: m.Name, Version: m.Version, Source: m.Source})
			}
		}
	}
	return out, roles, nil
}

// content walks the tree once, classifying each file as an inventory (by well-known
// name/location) or a playbook (a YAML sequence of plays). Hidden dirs are skipped.
func (c *Client) content() (playbooks []Playbook, inventories []Inventory, err error) {
	walkErr := fs.WalkDir(c.fsys, ".", func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			if p != "." && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if isInventoryPath(p) {
			inventories = append(inventories, Inventory{Path: p, Format: inventoryFormat(p)})
			return nil
		}
		if hasYAMLExt(p) {
			b, rerr := fs.ReadFile(c.fsys, p)
			if rerr != nil {
				return fmt.Errorf("ansible-automation content: read %s: %w", p, rerr)
			}
			if hosts, plays, ok := playbookPlays(b); ok {
				playbooks = append(playbooks, Playbook{Path: p, Plays: plays, Hosts: hosts})
			}
		}
		return nil
	})
	if walkErr != nil {
		return nil, nil, walkErr
	}
	return playbooks, inventories, nil
}

// isInventoryPath recognizes the conventional inventory names and any file under an
// inventory/ or inventories/ directory.
func isInventoryPath(p string) bool {
	switch path.Base(p) {
	case "hosts", "hosts.ini", "inventory", "inventory.ini", "inventory.yml", "inventory.yaml":
		return true
	}
	for d := path.Dir(p); d != "." && d != "/" && d != ""; d = path.Dir(d) {
		if b := path.Base(d); b == "inventory" || b == "inventories" {
			return true
		}
	}
	return false
}

// inventoryFormat classifies an inventory by extension; a plain `hosts`/`inventory`
// file with no extension is INI-style (Ansible's default).
func inventoryFormat(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".yml", ".yaml":
		return "yaml"
	}
	return "ini"
}

func hasYAMLExt(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".yml", ".yaml":
		return true
	}
	return false
}

// playbookPlays decides whether a YAML document is a playbook: a top-level sequence
// whose elements are plays — a mapping with a `hosts:` key, or an `import_playbook:`
// wrapper. It returns the targeted host patterns and the play count. A role task file
// (a sequence of task mappings with no `hosts`) and a requirements.yml (a mapping, not
// a sequence) both correctly fail this test.
func playbookPlays(b []byte) (hosts []string, plays int, ok bool) {
	var seq []map[string]yaml.Node
	if err := yaml.Unmarshal(b, &seq); err != nil || len(seq) == 0 {
		return nil, 0, false
	}
	seen := map[string]bool{}
	for _, play := range seq {
		_, isImport := play["import_playbook"]
		hn, hasHosts := play["hosts"]
		if !hasHosts && !isImport {
			continue
		}
		ok = true
		plays++
		if hasHosts && hn.Kind == yaml.ScalarNode && hn.Value != "" && !seen[hn.Value] {
			seen[hn.Value] = true
			hosts = append(hosts, hn.Value)
		}
	}
	return hosts, plays, ok
}
