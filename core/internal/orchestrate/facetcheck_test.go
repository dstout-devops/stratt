package orchestrate

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/dstout-devops/stratt/types"
)

// TestExpectationNotBefore covers the cert-expiry threshold (ADR-0030): the
// addressed RFC3339 timestamp must be at least `notBefore` in the future.
func TestExpectationNotBefore(t *testing.T) {
	mk := func(d time.Duration) json.RawMessage {
		return json.RawMessage(`{"notAfter":"` + time.Now().Add(d).UTC().Format(time.RFC3339) + `"}`)
	}
	exp := types.FacetExpectation{Namespace: "cert.expiry", Path: "notAfter", NotBefore: "360h"}

	// Healthy: expires in 720h (> 360h window) → met.
	if r := expectationUnmet(mk(720*time.Hour), exp); r != "" {
		t.Fatalf("cert 720h out should be met, got %q", r)
	}
	// Expiring: expires in 48h (< 360h window) → unmet.
	if r := expectationUnmet(mk(48*time.Hour), exp); r == "" {
		t.Fatal("cert 48h out should be within the renewal window (unmet)")
	}
	// Already expired → unmet.
	if r := expectationUnmet(mk(-time.Hour), exp); r == "" {
		t.Fatal("expired cert should be unmet")
	}
	// Malformed window → unmet (never silently clean, §1.8).
	if r := expectationUnmet(mk(720*time.Hour), types.FacetExpectation{Namespace: "cert.expiry", Path: "notAfter", NotBefore: "soon"}); r == "" {
		t.Fatal("bad window must be unmet, not clean")
	}
	// Non-timestamp value → unmet.
	bad := json.RawMessage(`{"notAfter":123}`)
	if r := expectationUnmet(bad, exp); r == "" {
		t.Fatal("non-timestamp notAfter must be unmet")
	}
}

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
