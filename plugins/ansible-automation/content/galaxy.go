package content

import (
	"errors"
	"io/fs"

	"gopkg.in/yaml.v3"
)

// ── the root that IS a collection (ANS-007) ──────────────────────────────────────────────────────
//
// A `galaxy.yml` at the root means the repo is not a loose pile of playbooks and roles — it is one
// collection, with an FQCN, a version, its own dependencies, and a layout where content lives in
// `plugins/` and `playbooks/` rather than beside the root. The audit's complaint is that such a
// repo "is not recognized as one — it projects as loose playbooks and roles", which is not merely
// incomplete: it is the wrong shape, and the thing an operator most needs to know about that repo
// (that it publishes `acme.platform` at 2.1.0) is absent.
//
// ── SAME KIND AS A REQUIRED COLLECTION, DISCRIMINATED BY A FIELD ────────────────────────────────
// The root collection lands as an `ansible.collection` beside the ones requirements.yml declares,
// marked `root: true` — the identical shape ANS-002 used for roles, for the identical reason: "what
// collections are in play here, and which one is THIS repo" is one question, and two Kinds would
// make it two queries and a join.
//
// Identity is the galaxy.yml PATH rather than the FQCN, and that is a deliberate small asymmetry
// with the role case. Required collections have been keyed by bare name since this Syncer shipped;
// prefixing both spaces to match roles/ would re-key every existing collection entity for a
// cosmetic gain. A path cannot collide with a bare FQCN, so the two spaces are already disjoint.

// GalaxyRoot is a content root's own galaxy.yml — the repo declaring itself a collection.
type GalaxyRoot struct {
	Path      string
	Namespace string
	Name      string
	Version   string
	// Dependencies are the collection FQCNs this collection requires, with their version
	// specs. A collection's dependencies live here rather than in requirements.yml, so a
	// projection that reads only requirements.yml sees none of them.
	Dependencies map[string]string
	Description  string
	License      []string
}

// FQCN is the fully-qualified collection name, the identity every consumer refers to it by.
func (g *GalaxyRoot) FQCN() string {
	if g.Namespace == "" || g.Name == "" {
		return g.Name
	}
	return g.Namespace + "." + g.Name
}

// readGalaxy parses a root's galaxy.yml, if it has one.
func (c *Client) readGalaxy() (*GalaxyRoot, error) {
	for _, p := range []string{"galaxy.yml", "galaxy.yaml"} {
		b, err := fs.ReadFile(c.fsys, p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var doc struct {
			Namespace    string            `yaml:"namespace"`
			Name         string            `yaml:"name"`
			Version      string            `yaml:"version"`
			Description  string            `yaml:"description"`
			License      []string          `yaml:"license"`
			Dependencies map[string]string `yaml:"dependencies"`
		}
		if yaml.Unmarshal(b, &doc) != nil {
			// A galaxy.yml that does not parse is not a collection root, and is not a reason
			// to fail the whole Observe — the rest of the tree still projects (§1.8).
			return nil, nil
		}
		if doc.Name == "" {
			return nil, nil // not a collection manifest, whatever else it is
		}
		return &GalaxyRoot{
			Path: p, Namespace: doc.Namespace, Name: doc.Name, Version: doc.Version,
			Dependencies: doc.Dependencies, Description: doc.Description, License: doc.License,
		}, nil
	}
	return nil, nil
}
