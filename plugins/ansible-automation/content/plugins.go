package content

import (
	"io/fs"
	"path"
	"sort"
	"strings"
)

// ── a repo's OWN code: custom modules and plugins (ANS-006) ──────────────────────────────────────
//
// The audit ranks this one by migration risk rather than by size: "a repo's own custom content is
// invisible; on a migration this is the content most likely to break." Everything else in a content
// root is Ansible-shaped and portable. A hand-written module in `library/` is Python that runs on
// the target, against a specific interpreter, using ansible's module API — and it is the thing an
// EE image either carries or does not.
//
// NAME, TYPE AND PATH. Never contents: this is Python (and occasionally Powershell), and parsing it
// would be reading a program to guess what it does — well past the §9 line the role-internals rule
// already draws. Existence and kind are structure; behaviour is not.

// pluginDirs maps a directory name to the plugin TYPE it holds. Both layouts appear: the classic
// adjacent-directory form (`library/`, `filter_plugins/`) that a playbook repo uses, and the
// collection form (`plugins/modules/`, `plugins/filter/`) that ANS-007's galaxy.yml roots use.
// Covering only one would report half of a collection-shaped repo as having no custom content.
var pluginDirs = map[string]string{
	// Classic playbook-repo layout.
	"library":            "module",
	"module_utils":       "module_utils",
	"filter_plugins":     "filter",
	"callback_plugins":   "callback",
	"action_plugins":     "action",
	"lookup_plugins":     "lookup",
	"test_plugins":       "test",
	"vars_plugins":       "vars",
	"strategy_plugins":   "strategy",
	"inventory_plugins":  "inventory",
	"connection_plugins": "connection",
	"cache_plugins":      "cache",
	"doc_fragments":      "doc_fragments",
}

// collectionPluginTypes are the subdirectory names under a collection's `plugins/`.
var collectionPluginTypes = map[string]string{
	"modules": "module", "module_utils": "module_utils", "filter": "filter",
	"callback": "callback", "action": "action", "lookup": "lookup", "test": "test",
	"vars": "vars", "strategy": "strategy", "inventory": "inventory",
	"connection": "connection", "cache": "cache", "doc_fragments": "doc_fragments",
}

// Plugin is one custom module or plugin file the repo ships itself.
type Plugin struct {
	Name string
	Path string
	// Type is module | module_utils | filter | callback | action | lookup | test | vars |
	// strategy | inventory | connection | cache | doc_fragments.
	Type string
	// Role is the role that owns it, when the file lives under roles/<name>/library/ — a
	// role-local module is a different migration risk from a repo-wide one, because it travels
	// with the role.
	Role string
}

// plugins walks the whole tree once looking for the plugin directories, in both layouts and both
// at the root and inside roles.
func (c *Client) plugins() ([]Plugin, error) {
	var out []Plugin
	err := fs.WalkDir(c.fsys, ".", func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			if p != "." && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		typ, role, ok := classifyPlugin(p)
		if !ok {
			return nil
		}
		out = append(out, Plugin{Name: path.Base(p), Path: p, Type: typ, Role: role})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// classifyPlugin decides whether a file is custom plugin content, and of what type.
//
// It matches on the DIRECTORY the file sits in rather than on the file's own name or contents:
// ansible loads plugins by directory, so the directory is the fact, and it is the one thing here
// that can be known without reading the program.
func classifyPlugin(p string) (typ, role string, ok bool) {
	segs := strings.Split(p, "/")
	if len(segs) < 2 {
		return "", "", false
	}
	// `__init__.py` and compiled artifacts are packaging, not content an operator migrates.
	base := segs[len(segs)-1]
	if base == "__init__.py" || strings.HasSuffix(base, ".pyc") || strings.HasPrefix(base, ".") {
		return "", "", false
	}
	for i := 0; i < len(segs)-1; i++ {
		// The collection layout: plugins/<type>/…
		if segs[i] == "plugins" && i+1 < len(segs)-1 {
			if t, found := collectionPluginTypes[segs[i+1]]; found {
				return t, roleOwning(segs, i), true
			}
			continue
		}
		if t, found := pluginDirs[segs[i]]; found {
			return t, roleOwning(segs, i), true
		}
	}
	return "", "", false
}

// roleOwning reports the role that owns a plugin directory at index i, when the path is
// roles/<name>/… — a role-local module travels with the role and is a different migration risk
// from a repo-wide one.
func roleOwning(segs []string, i int) string {
	if i >= 2 && segs[0] == "roles" {
		return segs[1]
	}
	return ""
}
