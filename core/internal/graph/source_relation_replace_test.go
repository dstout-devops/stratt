package graph

import (
	"context"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// TestRetractSourceRelationsExcept proves the ADR-0081 MF-2 mechanism: a Source's
// full-sync delete-and-replace of its OWN observed edges — the reparent case a
// global relation-GC would need but ADR-0059 still lacks. It also confirms the sweep
// stays scoped to the Source (never another's edges).
func TestRetractSourceRelationsExcept(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	src, err := store.RegisterSource(ctx, types.Source{Kind: "kubeservices", Name: "k8s"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.RegisterSource(ctx, types.Source{Kind: "kubeservices", Name: "k8s-2"})
	if err != nil {
		t.Fatal(err)
	}
	proj := store.NormalizerProjector()
	prov := types.Provenance{WriterKind: types.WriterSyncer, WriterRef: "kubeservices", SourceID: src.ID}

	// The reparent scenario: sync 1 had A provide S; sync 2 has B provide S, so both
	// A→S (stale) and B→S (fresh) exist for this source. A distinct service S2 is
	// provided by the OTHER source, and must survive the scoped sweep.
	ids, err := proj.UpsertEntities(ctx, prov, []EntityUpsert{
		{Kind: "application", IdentityKeys: map[string]string{"helm.release": "prod/a"}},
		{Kind: "application", IdentityKeys: map[string]string{"helm.release": "prod/b"}},
		{Kind: "service", IdentityKeys: map[string]string{"k8s.service": "prod/s"}},
		{Kind: "service", IdentityKeys: map[string]string{"k8s.service": "prod/s2"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, b, s, s2 := ids[0], ids[1], ids[2], ids[3]
	for _, from := range []string{a, b} {
		if err := proj.UpsertRelation(ctx, prov, "provides", from, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := proj.UpsertRelation(ctx,
		types.Provenance{WriterKind: types.WriterSyncer, WriterRef: "other", SourceID: other.ID},
		"provides", a, s2); err != nil {
		t.Fatal(err)
	}

	if got := count(t, store, `SELECT count(*) FROM graph.relation WHERE type='provides'`); got != 3 {
		t.Fatalf("setup: want 3 provides edges (A→S, B→S from src; A→S2 from other), got %d", got)
	}

	// Full-sync replace keeping only the fresh B→S for THIS source: the stale A→S is
	// swept; B→S kept; the OTHER source's A→S2 untouched.
	n, err := proj.RetractSourceRelationsExcept(ctx, src.ID, "provides", []string{b}, []string{s})
	if err != nil {
		t.Fatalf("retract: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 edge retracted (the stale A→S), got %d", n)
	}
	if got := count(t, store, `SELECT count(*) FROM graph.relation WHERE type='provides' AND prov_source_id='`+src.ID+`'`); got != 1 {
		t.Fatalf("this source must keep exactly the fresh B→S, got %d", got)
	}
	if got := count(t, store, `SELECT count(*) FROM graph.relation WHERE type='provides' AND prov_source_id='`+other.ID+`'`); got != 1 {
		t.Fatalf("the other source's A→S2 must be untouched, got %d", got)
	}
	_ = s2

	// RelationTypesBySource reflects the surviving type.
	relTypes, err := store.RelationTypesBySource(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(relTypes) != 1 || relTypes[0] != "provides" {
		t.Fatalf("relation types by source: %v", relTypes)
	}

	// Empty keep-set retracts all of the source's edges of the type (type-fully-gone).
	if _, err := proj.RetractSourceRelationsExcept(ctx, src.ID, "provides", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := count(t, store, `SELECT count(*) FROM graph.relation WHERE type='provides' AND prov_source_id='`+src.ID+`'`); got != 0 {
		t.Fatalf("empty keep-set must retract all this source's provides, got %d", got)
	}
}
