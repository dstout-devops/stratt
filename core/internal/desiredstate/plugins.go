package desiredstate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Admitted plugin estates (ADR-0137 D1/D3).
//
// A plugin owns its declarations and ships them in `plugins/<name>/estate/`; the
// adopting estate says which of those it admits, in `<root>/plugins.yaml`. That
// split is the ADR's central distinction — LOCALITY is the plugin's, AUTHORITY is
// the estate's — and it is why a plugin cannot install itself: an Actuator
// declaration carries a write ceiling, so self-installation would be a vendor
// granting itself authority.
//
// The mechanism is deliberately small. An admitted estate is not a new namespace,
// a new precedence layer, or a new Kind: its directories are simply ADDITIONAL
// search paths for the kinds ParseDir already reads, and everything merges into
// one flat set validated in one pass. That is what keeps a cross-tree reference —
// a Blueprint here routing to a Workflow the plugin ships — an ordinary reference
// rather than a special case.

// pluginsFile is <root>/plugins.yaml.
type pluginsFile struct {
	Plugins []admittedPlugin `yaml:"plugins"`
}

// admittedPlugin is one entry: a name for diagnosis, and the estate root to load.
type admittedPlugin struct {
	Name string `yaml:"name"`
	Path string `yaml:"path"`
}

// estateRoots returns the roots ParseDir should read, the estate's own first.
//
// A MISSING plugins.yaml is an empty admission list, not an error: an estate that
// admits no plugins is a normal estate, and every estate that predates this
// mechanism is one. A PRESENT but broken one is an error — the difference between
// "declared nothing" and "declared something unreadable" is exactly the difference
// §1.8 asks us not to blur.
func estateRoots(root string) ([]string, error) {
	roots := []string{root}

	path := filepath.Join(root, "plugins.yaml")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return roots, nil
	}
	if err != nil {
		return nil, fmt.Errorf("desiredstate: %s: %w", path, err)
	}

	var f pluginsFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true) // a typo in an admission must fail, never silently admit nothing
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("desiredstate: %s: %w", path, err)
	}

	seen := map[string]bool{}
	for _, p := range f.Plugins {
		if p.Name == "" {
			return nil, fmt.Errorf("desiredstate: %s: every admitted plugin needs a name", path)
		}
		if p.Path == "" {
			return nil, fmt.Errorf("desiredstate: %s: plugin %q needs a path to its estate", path, p.Name)
		}
		if seen[p.Name] {
			return nil, fmt.Errorf("desiredstate: %s: plugin %q is admitted twice", path, p.Name)
		}
		seen[p.Name] = true

		dir := filepath.Join(root, filepath.FromSlash(p.Path))
		// A path that does not exist fails the LOAD. The alternative — skipping it —
		// would let a typo in an admission read as a working estate that silently runs
		// none of that plugin's Workflows, which is the shape of failure this project
		// keeps finding and refusing (§1.8).
		info, err := os.Stat(dir)
		if err != nil {
			return nil, fmt.Errorf("desiredstate: %s: plugin %q estate %s: %w", path, p.Name, p.Path, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("desiredstate: %s: plugin %q estate %s is not a directory", path, p.Name, p.Path)
		}
		roots = append(roots, dir)
	}
	return roots, nil
}

// kindDirs maps the estate roots to the per-kind directories parseKind reads. The
// estate's own root comes first, so a diagnostic naming several files lists the
// estate before its plugins — the order a reader expects.
func kindDirs(roots []string, kind string) []string {
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		out = append(out, filepath.Join(r, kind))
	}
	return out
}

// estateRootOf recovers the estate root a declaration file came from:
// <root>/<kind>/<file>.yaml. It is derived from the path rather than threaded
// through parseKind because exactly one thing needs it — an Actuator's
// `contentDir`, which is relative to the estate root that SHIPPED that Actuator,
// not to the estate that admitted it (ADR-0137 D1: content travels with the
// plugin).
func estateRootOf(declPath string) string {
	return filepath.Dir(filepath.Dir(declPath))
}
