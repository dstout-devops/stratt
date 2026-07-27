package msgraph

import (
	"context"
	"sort"
	"testing"

	"github.com/dstout-devops/stratt/sdk/mockstratt"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// Port conformance for the msgraph Syncer's OBSERVE verb (sdk/mockstratt).
//
// The property under test is the one that had no home until ADR-0143: everything
// this plugin PROJECTS must appear in the Manifest's `contracts` advertisement.
// That advertisement is what an operator reads in order to write the grant, so a
// namespace emitted but never advertised either gets silently dropped or lands
// under authority nobody was asked for (§1.8/§2.5). Three sets exist — advertised,
// granted, emitted — and this pairing was checked nowhere.
//
// The fixture is deliberately POPULATED rather than minimal: a normalize that emits
// nothing makes this pass vacuously, which is how the first such test in this repo
// (vcenter's) went green for the wrong reason.

func TestObserveConformance(t *testing.T) {
	enabled := true
	e, err := normalizeDevice(device{
		ID: "obj-1", DeviceID: "dev-1", DisplayName: "WEB-01",
		OperatingSystem: "Windows", OperatingSystemVersion: "10.0.26100",
		AccountEnabled: &enabled, TrustType: "AzureAd", ProfileType: "RegisteredDevice",
		ApproxLastSignIn: "2026-07-01T12:00:00Z",
	})
	if err != nil {
		t.Fatalf("normalizeDevice: %v", err)
	}
	assertObserveConformance(t, e)
}

// assertObserveConformance runs the Observe suite over the entities a fixture produced,
// and — the part that matters — REFUSES to run vacuously.
//
// A conformance test that exercises only some of what a plugin advertises reports green
// while leaving the rest unguarded, which is not a hypothetical: the first two of these
// written in this repo both did it. vcenter's simulator sweep produced 21 entities and
// zero carrying the namespace it was written to guard, and salt's first fixture emitted
// 2 of the 4 namespaces salt advertises. Both passed. Both proved nothing about the
// namespaces they missed.
//
// So coverage is asserted, not assumed: the union of what the fixtures emit must include
// every namespace the Manifest advertises. Adding a namespace to a plugin now fails its
// conformance test until the fixture exercises it — which is the only way this check
// stays worth running.
func assertObserveConformance(t *testing.T, entities ...*pluginv1.ObservedEntity) {
	t.Helper()
	mres, err := (&Server{}).GetManifest(context.Background(), &pluginv1.GetManifestRequest{})
	if err != nil {
		t.Fatalf("GetManifest: %v", err)
	}
	m := mres.GetManifest()

	emitted := map[string]bool{}
	ents := make([]mockstratt.Entity, 0, len(entities))
	for _, e := range entities {
		for ns := range e.GetFacets() {
			emitted[ns] = true
		}
		ents = append(ents, mockstratt.Entity{
			Kind: e.GetKind(), IdentityKeys: e.GetIdentityKeys(),
			Labels: e.GetLabels(), Facets: e.GetFacets(),
		})
	}
	for _, cd := range m.GetContracts() {
		if !emitted[cd.GetSchemaId()] {
			t.Errorf("fixture never emits %q, which this plugin ADVERTISES — the conformance check "+
				"cannot judge a namespace no fixture produces, so that namespace is unguarded. "+
				"Populate the fixture (emitted: %v)", cd.GetSchemaId(), keysOf(emitted))
		}
	}

	conf := mockstratt.ObserveConformance{
		Result:   mockstratt.ObserveResult{Entities: ents, FullSyncComplete: true},
		Manifest: m,
	}
	if errs := conf.Errors(); len(errs) > 0 {
		t.Fatalf("port conformance violated:\n%s", conf.Report())
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
