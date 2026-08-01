package dns

import (
	"context"
	"sort"
	"testing"

	"github.com/dstout-devops/stratt/sdk/mockstratt"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// Port conformance for the dns Syncer's OBSERVE verb (sdk/mockstratt).
//
// The property under test is the one ADR-0143's own post-mortem named as unguarded:
// everything a plugin PROJECTS must appear in its Manifest's `contracts`. That
// advertisement is what an operator reads in order to write the grant, so a namespace
// emitted but never advertised either gets silently dropped at the governor or lands
// under authority nobody was asked for (§1.8/§2.5).
//
// The fixture is POPULATED rather than minimal, and refuses to run vacuously — the
// first two conformance tests written in this repo both went green while exercising
// none of the namespace they existed to guard.
func TestObserveConformance(t *testing.T) {
	// Both projection rules in one fixture: an A record (the Entity IS the name) and a
	// CNAME (the Entity is the canonical target, the alias is its coordinate). They
	// come from different branches of normalizeZone, so only their union covers it.
	ents, _ := normalizeZone("host", []Record{
		{Name: "web-01.estate.example", Type: "A", Data: "10.0.0.5"},
		{Name: "www.estate.example", Type: "CNAME", Data: "web-01.dev.stratt.test"},
	})
	if len(ents) != 2 {
		t.Fatalf("fixture produced %d entities, want 2 — a conformance run over a thin fixture proves nothing", len(ents))
	}
	assertObserveConformance(t, ents...)
}

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
				"cannot judge a namespace no fixture produces, so that namespace is unguarded "+
				"(emitted: %v)", cd.GetSchemaId(), keysOf(emitted))
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

// TestManifestGrantsNoTombstoneScheme pins a decision that reads like an omission and is
// not one (ADR-0144 D3). The only identity this plugin emits is `dns.fqdn`, which it
// SHARES with `declared` and `vcenter` — correlating onto their Entities is the entire
// mechanism. A tombstone scheme on a shared identity would let a zone that stopped
// mentioning a name delete the vCenter VM behind it, on a full sync, silently.
//
// Adding one must therefore be a deliberate act with an argument attached, not a line
// someone adds while making the Syncer "complete".
func TestManifestGrantsNoTombstoneScheme(t *testing.T) {
	mres, err := (&Server{}).GetManifest(context.Background(), &pluginv1.GetManifestRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if ts := mres.GetManifest().GetTombstoneSchemes(); len(ts) > 0 {
		t.Errorf("the dns Syncer advertises tombstone schemes %v — it emits only the SHARED dns.fqdn "+
			"identity, so tombstoning on it deletes other sources' Entities (ADR-0144 D3)", ts)
	}
}

// TestManifestAdvertisesEveryActionItServes closes the gap ADR-0092 found in helm: the
// verb was advertised, the contracts existed, the estate declared the actionNames — and
// the Manifest named no Action, so core registered dispatch on the estate's word alone.
// Dispatch worked, which is exactly why nothing surfaced it.
func TestManifestAdvertisesEveryActionItServes(t *testing.T) {
	mres, err := (&Server{}).GetManifest(context.Background(), &pluginv1.GetManifestRequest{})
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, a := range mres.GetManifest().GetActions() {
		declared[a.GetName()] = true
		if a.GetInput().GetSchemaId() == "" || a.GetInput().GetSha256() == "" {
			t.Errorf("action %q advertises an unpinned input Contract — the port's invariant #5 is a "+
				"HASH, and an empty one verifies nothing", a.GetName())
		}
	}
	for _, want := range []string{actionRegister, actionDeregister} {
		if !declared[want] {
			t.Errorf("Invoke serves %q but the Manifest does not advertise it", want)
		}
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
