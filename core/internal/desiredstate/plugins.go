package desiredstate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/dstout-devops/stratt/core/internal/contract"
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
	// ContractsOnly admits the plugin's SELF CONTRACTS without its declarations.
	//
	// The case is real and the relocation exposed it. demos/app-cert declares its OWN
	// `ansible-crypto` Actuator with `pluginIdentity: ansible`; since ADR-0138 D3/D4 that
	// Actuator's input Contract lives in the ansible plugin's tree, so the demo must admit
	// ansible to reference it — but admitting the ESTATE too imports ansible's Workflows, which
	// target Views the demo does not declare. It needs the tool's seam, not the tool's estate.
	//
	// Kept explicit rather than inferred from `pluginIdentity`, because admission is the
	// AUTHORITY half of ADR-0137 D1: an estate says what it uses, it is not deduced for it.
	ContractsOnly bool `yaml:"contractsOnly"`
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

	// The admission list is parsed OUTSIDE parseKind, so it needs the one-document rule applied
	// explicitly. It is also the worst place to lose a document: a dropped admission does not lose
	// one declaration, it loses every declaration that plugin ships — and the estate still loads.
	if err := refuseMultiDocument(path, raw); err != nil {
		return nil, err
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

		if p.ContractsOnly {
			// Contracts without declarations: loadPluginContracts reads it, this pass must not.
			// Adding its estate here would import exactly the Workflows the entry exists to avoid.
			continue
		}

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

// ── plugin-owned self contracts (ADR-0138 D3/D4) ─────────────────────────────

// loadPluginContracts registers every ADMITTED plugin's self contracts, read from
// `plugins/<name>/contracts/**/*.schema.json` — a sibling of its `estate/`, because a contract is
// the plugin's own document rather than a declaration the estate admits piecemeal.
//
// It runs BEFORE the kinds are parsed, and it has to: the same pass that discovers these documents
// is the one that validates Steps against them. A Step naming `actions/helm/deploy.input` is
// checked at load, so the document must be registered first or the check would refuse the very
// thing the plugin ships.
//
// Admission is the gate. A plugin tree that `plugins.yaml` does not admit contributes nothing —
// its contracts are as unread as its declarations, which is the whole point of the split: locality
// is the plugin's, AUTHORITY is the estate's (ADR-0137 D1).
func loadPluginContracts(root string) error {
	// A plugin's OWN tree carries its own contracts, admission or not. `plugins/<n>/estate` and
	// `plugins/<n>/demo/estate` are both inside <n>'s authority boundary — the demo is the plugin's
	// own harness, not a third party admitting it — so requiring a plugins.yaml there would make a
	// plugin unable to reference its own Actions in its own demo.
	if owner, dir, ok := owningPluginContracts(root); ok {
		docs, err := readContractDir(dir)
		if err != nil {
			return fmt.Errorf("desiredstate: plugin %s contracts: %w", owner, err)
		}
		if len(docs) > 0 {
			if err := contract.RegisterEstate(owner, docs); err != nil {
				return fmt.Errorf("desiredstate: plugin %s: %w", owner, err)
			}
		}
	}
	admitted, err := admittedPluginDirs(root)
	if err != nil {
		return err
	}
	for _, p := range admitted {
		dir := filepath.Join(filepath.Dir(p.estateDir), "contracts")
		docs, err := readContractDir(dir)
		if err != nil {
			return fmt.Errorf("desiredstate: plugin %s contracts: %w", p.name, err)
		}
		if len(docs) == 0 {
			continue
		}
		if err := contract.RegisterEstate(p.name, docs); err != nil {
			return fmt.Errorf("desiredstate: plugin %s: %w", p.name, err)
		}
	}
	return nil
}

// readContractDir reads a plugin's contracts tree into name → bytes, where the NAME is the path
// relative to the tree with `.schema.json` stripped — i.e. exactly the id core validates against
// (`actions/helm/deploy.input`). Keeping the layout identical to the shipped tree is deliberate:
// residence moved, the naming did not, so nothing downstream has to know where a document lives.
func readContractDir(dir string) (map[string][]byte, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}
	docs := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".schema.json") {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		docs[strings.TrimSuffix(filepath.ToSlash(rel), ".schema.json")] = raw
		return nil
	})
	return docs, err
}

// admittedPluginDirs returns the admitted plugins' names + estate dirs, so a caller can reach a
// sibling of the estate (the contracts tree) without re-parsing plugins.yaml.
type admittedDir struct {
	name string
	// estateDir is the admitted estate path. For a contractsOnly entry it still points at the
	// plugin's estate directory, because that is how the contracts sibling is located — the
	// entry changes what is LOADED, not where the plugin lives.
	estateDir string
}

func admittedPluginDirs(root string) ([]admittedDir, error) {
	path := filepath.Join(root, "plugins.yaml")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	var f pluginsFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	out := make([]admittedDir, 0, len(f.Plugins))
	for _, p := range f.Plugins {
		if p.Name == "" || p.Path == "" {
			continue // estateRoots reports these properly; this pass must not duplicate its errors
		}
		dir := p.Path
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(root, dir)
		}
		out = append(out, admittedDir{name: p.Name, estateDir: filepath.Clean(dir)})
	}
	return out, nil
}

// owningPluginContracts reports the plugin whose tree `root` sits inside, and where its contracts
// live. Recognised by structure — a directory whose PARENT is named `plugins` — rather than by a
// hardcoded depth, so `plugins/<n>/estate` and `plugins/<n>/demo/estate` are both found without
// enumerating the shapes a plugin tree may take.
func owningPluginContracts(root string) (owner, dir string, ok bool) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", "", false
	}
	for p := abs; ; {
		parent := filepath.Dir(p)
		if parent == p {
			return "", "", false
		}
		if filepath.Base(parent) == "plugins" {
			return filepath.Base(p), filepath.Join(p, "contracts"), true
		}
		p = parent
	}
}
