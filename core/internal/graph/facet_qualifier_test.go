package graph

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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

// A contended namespace resolves to the DECLARED AUTHORITY, and to nothing when none is declared.
//
// ADR-0152 follow-up 6, and the older half of the defect it closes. ADR-0060 gave the Facet grain a
// SOURCE dimension so many sources may project one namespace, and gave the scalar routing read the
// declared-authority collapse to resolve them. The Baseline evaluator never learned about it: it
// ranged GetFacets into a map keyed by namespace, so the LAST ROW SCANNED won — which observation a
// compliance check evaluated against was decided by Postgres's return order.
//
// Omitting a contended key is the honest direction to be wrong in. A drift check that silently
// evaluates one of two disagreeing observations reports compliance it cannot justify; one that
// reports the value as absent is visibly wrong, and the ownership-contention Finding names the
// contention itself (§2.4, §1.8).
func TestContendedFacetsResolveToTheDeclaredAuthorityOrToNothing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	id, _ := qualifierFixture(t, s, "custom.thing")
	// Two SYNCER owners of one namespace — the shape ADR-0060 exists for.
	for _, owner := range []string{"alpha/syncer", "beta/syncer"} {
		if err := s.RegisterFacetOwner(ctx, types.FacetOwner{
			Namespace: "custom.thing", OwnerKind: "syncer", OwnerRef: owner,
		}); err != nil {
			t.Fatalf("register %s: %v", owner, err)
		}
	}
	p := s.NormalizerProjector()
	write := func(owner, sourceID, port string) {
		t.Helper()
		pv := types.Provenance{WriterKind: types.WriterSyncer, WriterRef: owner, SourceID: sourceID, At: time.Now().UTC()}
		if err := p.UpsertFacet(ctx, pv, id, "custom.thing", json.RawMessage(`{"port":"`+port+`"}`)); err != nil {
			t.Fatalf("write as %s: %v", owner, err)
		}
	}
	write("alpha/syncer", testSourceID, "80")
	write("beta/syncer", "", "8080")

	key := FacetKey{Namespace: "custom.thing"}
	byKey, contended, err := s.ResolvedFacetsByEntity(ctx, id)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, ok := byKey[key]; ok {
		t.Error("two sources disagree and none is declared authoritative — the read must OMIT the " +
			"value rather than pick one, or a compliance check reports a verdict Postgres chose")
	}
	if len(contended) != 1 || contended[0] != key {
		t.Fatalf("the contention must be reported so the check can say why it could not evaluate; got %v", contended)
	}

	// Declare one authoritative → the read resolves to exactly that source's value.
	if err := s.RegisterFacetOwner(ctx, types.FacetOwner{
		Namespace: "custom.thing", OwnerKind: "syncer", OwnerRef: "beta/syncer", Authoritative: true,
	}); err != nil {
		t.Fatalf("declare authority: %v", err)
	}
	byKey, contended, err = s.ResolvedFacetsByEntity(ctx, id)
	if err != nil {
		t.Fatalf("resolve after authority: %v", err)
	}
	if len(contended) != 0 {
		t.Errorf("a declared authority clears the contention, got %v", contended)
	}
	var got struct {
		Port string `json:"port"`
	}
	if err := json.Unmarshal(byKey[key], &got); err != nil {
		t.Fatalf("decode resolved value: %v", err)
	}
	if got.Port != "8080" {
		t.Errorf("resolved port = %q, want 8080 (beta/syncer is the declared authority) — resolving to "+
			"the OTHER source would mean the authority declaration decides nothing", got.Port)
	}
}
