package ansibleproject

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

// Role is a directory under roles/ — reusable content a playbook includes.
type Role struct {
	Name string
	Path string
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
	if snap.Collections, err = c.collections(); err != nil {
		return nil, err
	}
	if snap.Playbooks, snap.Inventories, err = c.content(); err != nil {
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
		return nil, fmt.Errorf("ansibleproject: read roles/: %w", err)
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		out = append(out, Role{Name: e.Name(), Path: "roles/" + e.Name()})
	}
	return out, nil
}

// collections parses the Galaxy collection dependencies from the standard
// requirements.yml locations. Each entry is either a bare FQCN string or a
// {name, version, source} mapping (both Galaxy-legal forms).
func (c *Client) collections() ([]Collection, error) {
	var out []Collection
	for _, p := range []string{"requirements.yml", "requirements.yaml", "collections/requirements.yml", "collections/requirements.yaml"} {
		b, err := fs.ReadFile(c.fsys, p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("ansibleproject: read %s: %w", p, err)
		}
		var doc struct {
			Collections []yaml.Node `yaml:"collections"`
		}
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return nil, fmt.Errorf("ansibleproject: parse %s: %w", p, err)
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
	return out, nil
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
				return fmt.Errorf("ansibleproject: read %s: %w", p, rerr)
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
