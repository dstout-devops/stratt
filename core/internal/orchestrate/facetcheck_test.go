package orchestrate

import (
	"encoding/json"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func TestExpectationUnmet(t *testing.T) {
	kernel := json.RawMessage(`{"family":"linux","arch":"x86_64","modules":["a","b"]}`)

	// Equals met.
	if r := expectationUnmet(kernel, types.FacetExpectation{Namespace: "os.kernel", Path: "arch", Equals: json.RawMessage(`"x86_64"`)}); r != "" {
		t.Fatalf("arch=x86_64 should be met, got %q", r)
	}
	// Equals mismatch.
	if r := expectationUnmet(kernel, types.FacetExpectation{Namespace: "os.kernel", Path: "arch", Equals: json.RawMessage(`"arm64"`)}); r == "" {
		t.Fatal("arch mismatch should be unmet")
	}
	// Missing facet is unmet (desired state absent is drift).
	if r := expectationUnmet(nil, types.FacetExpectation{Namespace: "apps.installed", Equals: json.RawMessage(`"x"`)}); r != "facet absent" {
		t.Fatalf("absent facet: %q", r)
	}
	// Missing path is unmet.
	if r := expectationUnmet(kernel, types.FacetExpectation{Namespace: "os.kernel", Path: "nope", Equals: json.RawMessage(`"x"`)}); r != "path absent" {
		t.Fatalf("absent path: %q", r)
	}
	// Contains met (array membership).
	if r := expectationUnmet(kernel, types.FacetExpectation{Namespace: "os.kernel", Path: "modules", Contains: json.RawMessage(`"a"`)}); r != "" {
		t.Fatalf("modules contains a should be met, got %q", r)
	}
	// Contains unmet.
	if r := expectationUnmet(kernel, types.FacetExpectation{Namespace: "os.kernel", Path: "modules", Contains: json.RawMessage(`"z"`)}); r == "" {
		t.Fatal("modules does not contain z")
	}
}

func TestFacetAtPath(t *testing.T) {
	doc := json.RawMessage(`{"a":{"b":{"c":42}}}`)
	got, ok := facetAtPath(doc, "a.b.c")
	if !ok || string(got) != "42" {
		t.Fatalf("nested path: %s ok=%v", got, ok)
	}
	if _, ok := facetAtPath(doc, "a.x"); ok {
		t.Fatal("missing path must report absent")
	}
	whole, ok := facetAtPath(doc, "")
	if !ok || !jsonEqual(whole, doc) {
		t.Fatal("empty path returns the whole document")
	}
}
