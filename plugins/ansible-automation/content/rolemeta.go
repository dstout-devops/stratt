package content

import (
	"errors"
	"io/fs"
	"path"

	"gopkg.in/yaml.v3"
)

// ── a role's meta/main.yml: the one graph inside a role (ANS-004) ────────────────────────────────
//
// The audit holds a line here that is worth restating rather than eroding: role INTERNALS —
// tasks, handlers, defaults, vars — are deliberately not read, because that is Ansible's execution
// model and reinterpreting it is the §9 no-new-language line. `meta/main.yml` is on the other side
// of that line and the audit says so: a role's DECLARED DEPENDENCIES are structure, not execution
// semantics, and they are "the one thing inside a role worth projecting". Observing them
// reinterprets nothing — it draws an edge Ansible already wrote down.
//
// The dependency edge is what makes a role graph traversable: "what breaks if I change this role"
// has no answer without it, and that is the question a migration asks first.

// roleMeta is the subset of meta/main.yml this projects.
type roleMeta struct {
	Dependencies []roleDep `yaml:"dependencies"`
	GalaxyInfo   struct {
		Author            string `yaml:"author"`
		License           string `yaml:"license"`
		MinAnsibleVersion string `yaml:"min_ansible_version"`
		Platforms         []struct {
			Name string `yaml:"name"`
		} `yaml:"platforms"`
	} `yaml:"galaxy_info"`
}

// roleDep is one dependency entry. Galaxy allows a bare string OR a mapping keyed `role` or
// `name`, and all three forms appear in real repos — accepting only the mapping would silently
// drop the dependencies of every role written the short way.
type roleDep struct {
	Name string
}

func (d *roleDep) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		d.Name = n.Value
		return nil
	}
	var m struct {
		Role string `yaml:"role"`
		Name string `yaml:"name"`
		Src  string `yaml:"src"`
	}
	if err := n.Decode(&m); err != nil {
		// A dependency shape we do not recognize is skipped, not fatal: a malformed meta file
		// must not take the whole content root's projection down with it (§1.8).
		return nil
	}
	switch {
	case m.Role != "":
		d.Name = m.Role
	case m.Name != "":
		d.Name = m.Name
	default:
		d.Name = m.Src
	}
	return nil
}

// readRoleMeta fills a Role's meta-derived fields. A role with no meta/main.yml is the common
// case and is not an error.
func (c *Client) readRoleMeta(r *Role) error {
	b, err := fs.ReadFile(c.fsys, path.Join(r.Path, "meta", "main.yml"))
	if errors.Is(err, fs.ErrNotExist) {
		b, err = fs.ReadFile(c.fsys, path.Join(r.Path, "meta", "main.yaml"))
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var meta roleMeta
	if yaml.Unmarshal(b, &meta) != nil {
		return nil // unparseable meta is no meta, not a failed sync
	}
	for _, d := range meta.Dependencies {
		if d.Name != "" {
			r.Dependencies = append(r.Dependencies, d.Name)
		}
	}
	r.Author = meta.GalaxyInfo.Author
	r.License = meta.GalaxyInfo.License
	r.MinAnsibleVersion = meta.GalaxyInfo.MinAnsibleVersion
	for _, p := range meta.GalaxyInfo.Platforms {
		if p.Name != "" {
			r.Platforms = append(r.Platforms, p.Name)
		}
	}
	return nil
}

// ── requirements.yml's roles half (ANS-002) ──────────────────────────────────────────────────────
//
// Only `collections:` was parsed; `roles:` was not, so a project's role dependencies were invisible
// while its collection dependencies were not — an asymmetry inside one file.
//
// A REQUIRED role lands as an `ansible.role` alongside the in-tree ones, marked `required`, rather
// than as its own Kind. "Which roles does this project use, and which come from outside the repo?"
// is ONE question, and two Kinds would make it two queries and a join. The distinction that
// matters is carried by a field — which is also what lets a dependency EDGE from an in-tree role
// resolve to a required one without knowing which it is.

// parseRequirementsRoles parses the `roles:` half of a requirements file.
//
// PER-ENTRY, exactly as the collections half already does, and that is not stylistic. Galaxy
// allows a bare name string OR a mapping, and real files MIX THEM in one list — the first
// version of this decoded the whole `roles:` list into one struct shape, which fails on the
// first bare string and takes every correctly-formed entry down with it. Measured against
// yaml.v3: the list `[{name: geerlingguy.apache}, monitoring]` produces
//
//	cannot unmarshal !!str `monitoring` into struct { Name string; Version string }
//
// and the whole half projects as empty — the silent-drop shape rather than a visible failure.
func parseRequirementsRoles(doc *yaml.Node) []Role {
	var shape struct {
		Roles []yaml.Node `yaml:"roles"`
	}
	if doc.Decode(&shape) != nil {
		return nil
	}
	out := make([]Role, 0, len(shape.Roles))
	for _, n := range shape.Roles {
		if n.Kind == yaml.ScalarNode {
			if n.Value != "" {
				out = append(out, Role{Name: n.Value, Required: true})
			}
			continue
		}
		var m struct {
			Name    string `yaml:"name"`
			Src     string `yaml:"src"`
			Version string `yaml:"version"`
			SCM     string `yaml:"scm"`
		}
		if n.Decode(&m) != nil {
			continue // one unrecognized entry is skipped; the rest still project
		}
		name := m.Name
		if name == "" {
			name = m.Src // `- src: geerlingguy.apache` with no explicit name
		}
		if name == "" {
			continue
		}
		out = append(out, Role{Name: name, Version: m.Version, Source: m.Src, Required: true})
	}
	return out
}
