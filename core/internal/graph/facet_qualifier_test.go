package graph

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// qualifierFixture registers a namespace owner and returns one host Entity to hang Facets on.
func qualifierFixture(t *testing.T, s *Store, namespace string) (string, *Projector) {
	t.Helper()
	ctx := context.Background()
	if err := s.RegisterFacetOwner(ctx, types.FacetOwner{
		Namespace: namespace, OwnerKind: "team", OwnerRef: "platform",
	}); err != nil {
		t.Fatalf("register owner: %v", err)
	}
	p := s.NormalizerProjector()
	ids, err := p.UpsertEntities(ctx, prov(types.WriterSyncer, "vcenter/syncer"), []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"vcenter.uuid": "qualifier-host"}},
	})
	if err != nil {
		t.Fatalf("upsert entity: %v", err)
	}
	return ids[0], s.RunProjector()
}

// A qualified Facet round-trips through the write path and comes back with its qualifier.
//
// The EXPAND half of ADR-0152 (migration 00047): the column exists, the write path carries it, and
// every read surfaces it. What this release deliberately CANNOT do is hold two qualifiers on one
// (entity, namespace, source) — facet_pkey is still three columns, because the previous release's
// replicas upsert through that exact constraint and dropping it mid-rollout breaks them (ADR-0078).
// The contract release folds the column in; see TestASecondQualifierNeedsTheContractRelease below,
// which pins that limit so it is found by a failing test rather than by a surprise in production.
func TestAQualifiedFacetRoundTrips(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	id, run := qualifierFixture(t, s, "custom.thing")

	if err := run.UpsertQualifiedFacet(ctx, prov(types.WriterRun, "run-1"), id,
		"custom.thing", "tomcat", json.RawMessage(`{"port":"8080"}`)); err != nil {
		t.Fatalf("upsert qualified facet: %v", err)
	}
	facets, err := s.GetFacets(ctx, id)
	if err != nil {
		t.Fatalf("get facets: %v", err)
	}
	if len(facets) != 1 {
		t.Fatalf("expected 1 facet, got %d", len(facets))
	}
	if facets[0].Qualifier != "tomcat" {
		t.Errorf("qualifier did not survive the round trip: got %q, want tomcat. A qualifier that "+
			"vanishes on read is worse than none — the claim that owns the row becomes unidentifiable",
			facets[0].Qualifier)
	}
}

// An unqualified write is unqualified, and that is the ordinary case.
//
// Asserted rather than assumed, because the default is what every shipped namespace relies on: if
// UpsertFacet started stamping anything but the empty string, every existing Facet would change
// identity on its next write and every scalar read would begin suppressing it.
func TestAnOrdinaryFacetIsUnqualified(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	id, run := qualifierFixture(t, s, "custom.thing")

	if err := run.UpsertFacet(ctx, prov(types.WriterRun, "run-1"), id,
		"custom.thing", json.RawMessage(`{"port":"80"}`)); err != nil {
		t.Fatalf("upsert facet: %v", err)
	}
	facets, err := s.GetFacets(ctx, id)
	if err != nil {
		t.Fatalf("get facets: %v", err)
	}
	if facets[0].Qualifier != "" {
		t.Errorf("UpsertFacet must write the EMPTY qualifier, got %q", facets[0].Qualifier)
	}
	// And a scalar read resolves it, since it is unqualified.
	vals, suppressed, err := s.FacetValuesByEntitiesScoped(ctx, "custom.thing", []string{id})
	if err != nil {
		t.Fatalf("scalar read: %v", err)
	}
	if _, ok := vals[id]; !ok {
		t.Error("an unqualified facet must still resolve through the scalar read path")
	}
	if len(suppressed) != 0 {
		t.Errorf("nothing was suppressed, so nothing should be reported: got %v", suppressed)
	}
}

// A SCALAR read of a QUALIFIED namespace returns nothing — AND SAYS SO.
//
// This is ADR-0152 D5, and the "and says so" half is the part charter review put there. Absent is
// not a diagnosis: ResolveTargets turns a missing mgmt.address into Target{Address: ""} with no
// error, no event and no Finding, and routed dispatch turns a missing mgmt.site into LocalSite
// silently — a Run at the wrong locus. Today's omit-rather-than-pick is honest only because the
// ownership-contention Finding surfaces it, and D5 deliberately stops that Finding firing on
// qualifier multiplicity. So the suppression has to carry its own signal or a coordinate vanishes
// with zero diagnosis anywhere in the system, which is the exact failure ADR-0054 warns about.
func TestAScalarReadOfAQualifiedNamespaceIsSuppressedAndReported(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	id, run := qualifierFixture(t, s, "custom.thing")

	if err := run.UpsertQualifiedFacet(ctx, prov(types.WriterRun, "run-1"), id,
		"custom.thing", "tomcat", json.RawMessage(`{"port":"8080"}`)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	vals, suppressed, err := s.FacetValuesByEntitiesScoped(ctx, "custom.thing", []string{id})
	if err != nil {
		t.Fatalf("scalar read: %v", err)
	}
	if _, ok := vals[id]; ok {
		t.Error("a scalar read must NOT resolve a qualified namespace — picking one of several is " +
			"the silent precedence the qualifier exists to abolish (§2.4)")
	}
	if got := suppressed[id]; len(got) != 1 || got[0] != "tomcat" {
		t.Errorf("the suppression must name the qualifiers present so the caller can say WHY the "+
			"value is gone (§1.8); got %v", got)
	}
}

// A qualified Facet does not enter the {{.entity.*}} tree, and the omission is reported.
//
// `{{.entity.app.config.port}}` cannot mean two things. Picking one is silent precedence; nesting
// under an invented `…app.config.<qualifier>.port` level collides with any real sub-path of that
// name. So the namespace is refused ENTRY — but only that namespace: failing the WHOLE projection
// (which is what the reserved-key rule does) would break every binding on a host that legitimately
// runs two applications, including a certificate subject reading {{.entity.dns.fqdn}} that is
// entirely unaffected. ADR-0150 D2 binds subjects out of this tree, so that would be an outage.
func TestAQualifiedFacetIsKeptOutOfTheTemplateTreeAndNamed(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	id, run := qualifierFixture(t, s, "custom.thing")
	if err := s.RegisterFacetOwner(ctx, types.FacetOwner{
		Namespace: "dns", OwnerKind: "team", OwnerRef: "platform",
	}); err != nil {
		t.Fatalf("register dns owner: %v", err)
	}
	if err := run.UpsertQualifiedFacet(ctx, prov(types.WriterRun, "run-1"), id,
		"custom.thing", "tomcat", json.RawMessage(`{"port":"8080"}`)); err != nil {
		t.Fatalf("upsert qualified: %v", err)
	}
	if err := run.UpsertFacet(ctx, prov(types.WriterRun, "run-1"), id,
		"dns", json.RawMessage(`{"fqdn":"web-02.example"}`)); err != nil {
		t.Fatalf("upsert unqualified: %v", err)
	}

	tree, omitted, err := s.EntityTemplateNamespaceScoped(ctx, id)
	if err != nil {
		t.Fatalf("template namespace: %v", err)
	}
	if _, present := tree["custom"]; present {
		t.Error("a qualified namespace must not be placed in the {{.entity.*}} tree")
	}
	if got := omitted["custom.thing"]; len(got) != 1 || got[0] != "tomcat" {
		t.Errorf("the omission must name the namespace and its qualifiers; got %v", omitted)
	}
	// THE POINT: the rest of the Entity still binds. A host running two applications must not lose
	// its certificate subject.
	dns, ok := tree["dns"].(map[string]any)
	if !ok || dns["fqdn"] != "web-02.example" {
		t.Errorf("an unrelated unqualified facet must still resolve, got %v", tree["dns"])
	}
}

// TWO qualifiers on one (entity, namespace, source) are NOT yet storable, by design.
//
// Pinned as a test rather than left as a comment, because it is the whole reason ADR-0152 ships in
// two releases and the whole reason the estate loader currently REFUSES a declared qualifier. While
// facet_pkey is (entity_id, namespace, prov_source_id), the second write matches the first row and
// DO UPDATEs it — so the row's qualifier FLIPS rather than a second row appearing. Not an error:
// a silent last-writer-wins, which is precisely what D4 says the grain change exists to abolish.
//
// When the contract release folds the qualifier into the key, this test's expectation inverts —
// two rows, both retained — and its failure is the signal that the fold worked.
func TestASecondQualifierNeedsTheContractRelease(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	id, run := qualifierFixture(t, s, "custom.thing")

	for _, q := range []string{"apache", "tomcat"} {
		if err := run.UpsertQualifiedFacet(ctx, prov(types.WriterRun, "run-1"), id,
			"custom.thing", q, json.RawMessage(`{"port":"80"}`)); err != nil {
			t.Fatalf("upsert %s: %v", q, err)
		}
	}
	facets, err := s.GetFacets(ctx, id)
	if err != nil {
		t.Fatalf("get facets: %v", err)
	}
	if len(facets) != 1 || facets[0].Qualifier != "tomcat" {
		t.Fatalf("EXPAND-release behaviour changed: expected the single row to have been overwritten "+
			"to qualifier tomcat, got %d row(s) %v.\n\nIf the CONTRACT migration has landed, this is "+
			"the expected inversion — two rows should now coexist. Update this test to assert that, "+
			"and lift the estate-load refusal in desiredstate (blueprint route observe.qualifier).",
			len(facets), facets)
	}
}
