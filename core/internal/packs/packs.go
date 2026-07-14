// Package packs loads and materializes Stratt's in-tree content packs
// (ADR-0033). A pack is a curated grouping of existing Named Kinds — a
// collector Trigger that projects os.hardening.* Facets (charter §1.2) and
// facet-observation Baselines that assert the expected values. "Pack" is not a
// Named Kind (§2): this is authoring/distribution only.
//
// This mirrors core/internal/contract over contracts/: the packs/ module is
// hash-pinned DATA; the logic (load, content hash, placeholder substitution)
// lives here. Packs are materialized into the operator's desired-state Git
// (§1.2 stays operator-owned) by `stratt pack install`.
package packs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	packsfs "github.com/dstout-devops/stratt/packs"
)

// Parameter is one install-time placeholder a pack declares.
type Parameter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Default     string `yaml:"default"`
	Required    bool   `yaml:"required"`
}

// Manifest is a pack's manifest.yaml.
type Manifest struct {
	Name           string      `yaml:"name"`
	Title          string      `yaml:"title"`
	Version        int         `yaml:"version"`
	Description    string      `yaml:"description"`
	RequiredFacets []string    `yaml:"requiredFacets"`
	Parameters     []Parameter `yaml:"parameters"`
}

// Pack is a loaded pack: its manifest, a content hash pin over every file
// (charter §1.5), and the raw template files keyed by pack-relative path
// (e.g. "triggers/cis-hardening-collector.yaml").
type Pack struct {
	Manifest
	ContentHash string
	Files       map[string][]byte
}

// placeholder matches ${NAME} substitution tokens.
var placeholder = regexp.MustCompile(`\$\{([A-Z0-9_]+)\}`)

// List returns the manifest of every embedded pack, sorted by name.
func List() ([]Manifest, error) {
	entries, err := fs.ReadDir(packsfs.FS, ".")
	if err != nil {
		return nil, err
	}
	var out []Manifest
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := readManifest(e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func readManifest(name string) (Manifest, error) {
	raw, err := fs.ReadFile(packsfs.FS, path.Join(name, "manifest.yaml"))
	if err != nil {
		return Manifest{}, fmt.Errorf("packs: %s: no manifest.yaml: %w", name, err)
	}
	var m Manifest
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("packs: %s manifest: %w", name, err)
	}
	if m.Name != name {
		return Manifest{}, fmt.Errorf("packs: %s manifest declares name %q", name, m.Name)
	}
	return m, nil
}

// Load reads a pack, computing its content hash over every file (manifest
// included) in sorted path order so the pin is deterministic (§1.5).
func Load(name string) (Pack, error) {
	m, err := readManifest(name)
	if err != nil {
		return Pack{}, err
	}
	files := map[string][]byte{}
	err = fs.WalkDir(packsfs.FS, name, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || path.Base(p) == "manifest.yaml" {
			return nil
		}
		raw, rerr := fs.ReadFile(packsfs.FS, p)
		if rerr != nil {
			return rerr
		}
		rel := strings.TrimPrefix(p, name+"/")
		files[rel] = raw
		return nil
	})
	if err != nil {
		return Pack{}, err
	}
	if len(files) == 0 {
		return Pack{}, fmt.Errorf("packs: %s has no content files", name)
	}

	// Hash the manifest too, so a manifest edit moves the pin.
	manifestRaw, _ := fs.ReadFile(packsfs.FS, path.Join(name, "manifest.yaml"))
	h := sha256.New()
	paths := append(make([]string, 0, len(files)+1), "manifest.yaml")
	for rel := range files {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	for _, rel := range paths {
		body := files[rel]
		if rel == "manifest.yaml" {
			body = manifestRaw
		}
		fmt.Fprintf(h, "%s\x00%d\x00", rel, len(body))
		h.Write(body)
	}
	return Pack{Manifest: m, ContentHash: hex.EncodeToString(h.Sum(nil)), Files: files}, nil
}

// Materialize substitutes the pack's ${NAME} placeholders from params and
// returns the resolved files by pack-relative path. It applies parameter
// defaults, errors on a missing required parameter, and errors if any
// placeholder is left unsubstituted (a typo or an undeclared token) so a
// broken install fails loudly rather than emitting invalid CaC (§1.8).
func (p Pack) Materialize(params map[string]string) (map[string][]byte, error) {
	resolved := map[string]string{}
	for _, param := range p.Parameters {
		v, ok := params[param.Name]
		switch {
		case ok && v != "":
			resolved[param.Name] = v
		case param.Default != "":
			resolved[param.Name] = param.Default
		case param.Required:
			return nil, fmt.Errorf("packs: %s: required parameter %s not set", p.Name, param.Name)
		default:
			resolved[param.Name] = ""
		}
	}

	out := map[string][]byte{}
	for rel, raw := range p.Files {
		var missing string
		sub := placeholder.ReplaceAllStringFunc(string(raw), func(tok string) string {
			key := tok[2 : len(tok)-1]
			if v, ok := resolved[key]; ok {
				return v
			}
			missing = key
			return tok
		})
		if missing != "" {
			return nil, fmt.Errorf("packs: %s: %s references undeclared parameter %s", p.Name, rel, missing)
		}
		out[rel] = []byte(sub)
	}
	return out, nil
}
