package packs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/desiredstate"
)

func TestListAndLoadCIS(t *testing.T) {
	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, m := range list {
		if m.Name == "cis" {
			found = true
		}
	}
	if !found {
		t.Fatalf("cis pack not listed: %+v", list)
	}

	p, err := Load("cis")
	if err != nil {
		t.Fatal(err)
	}
	if p.Title == "" || p.Version < 1 || len(p.RequiredFacets) == 0 {
		t.Fatalf("manifest: %+v", p.Manifest)
	}
	if len(p.ContentHash) != 64 {
		t.Fatalf("content hash: %q", p.ContentHash)
	}
	// The collector Trigger and a broad Baseline set are present.
	if _, ok := p.Files["triggers/cis-hardening-collector.yaml"]; !ok {
		t.Fatalf("collector trigger missing: %v", keys(p.Files))
	}
	var baselines int
	for rel := range p.Files {
		if strings.HasPrefix(rel, "baselines/") {
			baselines++
		}
	}
	if baselines < 20 {
		t.Fatalf("expected a broad baseline set, got %d", baselines)
	}

	// The pin is deterministic across loads (§1.5).
	again, err := Load("cis")
	if err != nil {
		t.Fatal(err)
	}
	if again.ContentHash != p.ContentHash {
		t.Fatalf("content hash not stable: %s vs %s", p.ContentHash, again.ContentHash)
	}
}

func TestMaterializeRequiresView(t *testing.T) {
	p, err := Load("cis")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Materialize(map[string]string{}); err == nil {
		t.Fatal("materialize without required VIEW must fail")
	}
}

// TestMaterializeProducesValidCaC is the load-bearing round-trip: the pack must
// emit desired state the platform actually accepts. Materialize with a View,
// write the files into a declarations dir, and parse them through the real
// desiredstate parser — no placeholders may survive, and every Baseline +
// the collector Trigger must validate.
func TestMaterializeProducesValidCaC(t *testing.T) {
	p, err := Load("cis")
	if err != nil {
		t.Fatal(err)
	}
	files, err := p.Materialize(map[string]string{"VIEW": "prod-linux"})
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	// A View the materialized Baselines/Trigger reference.
	writeFile(t, filepath.Join(root, "views", "prod-linux.yaml"),
		"name: prod-linux\nselector: {facets: [{namespace: os.kernel, path: family, equals: linux}]}\n")
	for rel, body := range files {
		if strings.Contains(string(body), "${") {
			t.Fatalf("unsubstituted placeholder in %s:\n%s", rel, body)
		}
		writeFile(t, filepath.Join(root, rel), string(body))
	}

	decls, err := desiredstate.ParseDir(root)
	if err != nil {
		t.Fatalf("materialized pack is not valid CaC: %v", err)
	}
	if len(decls.Baselines) < 20 {
		t.Fatalf("baselines parsed: %d", len(decls.Baselines))
	}
	for _, b := range decls.Baselines {
		if b.Mode != "facet-observation" || b.Framework != "cis" || b.ViewName != "prod-linux" {
			t.Fatalf("baseline not materialized correctly: %+v", b)
		}
	}
	if len(decls.Triggers) != 1 || decls.Triggers[0].ViewName != "prod-linux" {
		t.Fatalf("collector trigger: %+v", decls.Triggers)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
