package authz

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestINV3_AuthzConsultsNoGraph enforces ADR-0079 INV-3: authorization evaluation
// traverses ZERO graph Relations — the `authenticates-as` / `member-of` identity
// edges are correlation-only, read by Views/Findings and NEVER by the authz
// evaluator. The invariant is structural: the authz package must not depend on the
// graph package at all. The Authorizer interface takes only strings
// (principalID/relation/object) — no graph handle — so a decision *cannot* consult
// a graph edge. This test is the tripwire: the day someone wires the identity
// projection into an access decision (the plane-merge ADR-0009 forbids), the authz
// package gains a graph import and this fails.
func TestINV3_AuthzConsultsNoGraph(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	const forbidden = "core/internal/graph"
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		// The invariant is about production code; test files may import graph to set
		// up cross-plane behavioral checks. But no such import exists today, and this
		// test file itself must not trip the guard — skip _test.go.
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(path, forbidden) {
				t.Fatalf("INV-3 violation: %s imports %q — authorization must NEVER consult the graph "+
					"(the identity projection's member-of/authenticates-as edges are correlation-only). "+
					"An access decision that traverses a graph Relation makes the graph load-bearing for "+
					"authz and a second truth (ADR-0079 INV-3, ADR-0009).", name, path)
			}
		}
	}
}
