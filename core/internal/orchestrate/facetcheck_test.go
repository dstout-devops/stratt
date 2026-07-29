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

// TestChartDriftNeedsAScalarFacet is the measurement behind app.deliverable existing at all
// (ADR-0148 follow-up b), written as a test because the alternative is a comment nobody can check.
//
// The CHART delivery form has to answer "is the deployed chart at the version the Intent asked
// for". software.chart is the obvious place to ask — it is the facet that carries the chart's name
// and version — and it CANNOT answer, for two independent reasons this pins:
//
//   - facetAtPath walks MAPS. `charts` is an array, so `charts.version` resolves to nothing and the
//     expectation is unmet for a release that is deployed at exactly the right version.
//   - jsonContains matches a whole element by DeepEqual. So the only expressible form makes the
//     estate enumerate every field the Normalizer happened to set — including appVersion, which is
//     a fact about the chart rather than desired state the estate could sensibly declare
//     (ADR-0148 D3).
//
// Neither is a bug to fix here: the list shape is what lets ONE form-agnostic advisory pass cover
// packages, containers and charts (ADR-0080), and widening the expectation language into element
// predicates is the grammar ADR-0085 refuses by name (§9). So the chart form gets the split the
// package form always had — app.config scalar + software.package list — and this test is what
// stops someone "simplifying" app.deliverable away.
func TestChartDriftNeedsAScalarFacet(t *testing.T) {
	const chartFacet = `{"charts":[{"name":"podinfo","version":"6.9.2","appVersion":"6.9.2","deliveryForm":"chart"}]}`
	const deliverableFacet = `{"name":"podinfo","version":"6.9.2"}`

	// ── software.chart cannot express the question ──────────────────────────────────────
	if r := expectationUnmet(json.RawMessage(chartFacet), types.FacetExpectation{
		Namespace: "software.chart", Path: "charts.version", Equals: json.RawMessage(`"6.9.2"`),
	}); r == "" {
		t.Fatal("charts.version must NOT resolve — facetAtPath walks maps, and a dotted path that " +
			"silently traversed an array would make this facet look usable for drift when it is not")
	}
	if r := expectationUnmet(json.RawMessage(chartFacet), types.FacetExpectation{
		Namespace: "software.chart", Path: "charts",
		Contains: json.RawMessage(`{"name":"podinfo","version":"6.9.2"}`),
	}); r == "" {
		t.Fatal("a PARTIAL component must not match: jsonContains is whole-element DeepEqual, so " +
			"anything that passed here would mean the estate could ask about one field of a component")
	}
	// It matches only when the estate restates the whole component — including appVersion, which no
	// Intent can know. This is the brittleness, asserted rather than described.
	if r := expectationUnmet(json.RawMessage(chartFacet), types.FacetExpectation{
		Namespace: "software.chart", Path: "charts",
		Contains: json.RawMessage(`{"name":"podinfo","version":"6.9.2","appVersion":"6.9.2","deliveryForm":"chart"}`),
	}); r != "" {
		t.Fatalf("whole-element containment should match, so the cost of the only expressible form "+
			"is visible: %s", r)
	}

	// ── app.deliverable does, on all three answers ──────────────────────────────────────
	met := types.FacetExpectation{Namespace: "app.deliverable", Path: "version", Equals: json.RawMessage(`"6.9.2"`)}
	if r := expectationUnmet(json.RawMessage(deliverableFacet), met); r != "" {
		t.Fatalf("the desired version IS deployed and the expectation must be met: %s", r)
	}
	if r := expectationUnmet(json.RawMessage(deliverableFacet), types.FacetExpectation{
		Namespace: "app.deliverable", Path: "version", Equals: json.RawMessage(`"6.9.1"`),
	}); r == "" {
		t.Fatal("a DIFFERENT deployed version must be drift — an expectation that cannot come back " +
			"wrong is not an expectation (ADR-0148 D4)")
	}
	// kubeservices omits `version` rather than guessing when the helm.sh/chart label carries none.
	// Unmet is the honest answer to "is the desired version deployed" when the version is unknown.
	if r := expectationUnmet(json.RawMessage(`{"name":"podinfo"}`), met); r == "" {
		t.Fatal("an absent version must be unmet, not met — a Finding resolved on a missing " +
			"observation is the echo failure ANS-014 was about")
	}
}
