package graph

import (
	"context"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// TestRelationPresenceReplace proves ADR-0082 relation liveness: a Source's full-sync
// presence replace collects an edge it drops (the reparent case) BUT a co-asserted
// edge survives because another Source still asserts it (cross-source union liveness,
// the edge analog of ADR-0042). Also confirms the sweep never touches another Source.
func TestRelationPresenceReplace(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	src, err := store.RegisterSource(ctx, types.Source{Kind: "kubeservices", Name: "k8s"})
	if err != nil {
		t.Fatal(err)
	}
	mesh, err := store.RegisterSource(ctx, types.Source{Kind: "mesh", Name: "istio"})
	if err != nil {
		t.Fatal(err)
	}
	proj := store.NormalizerProjector()
	srcProv := types.Provenance{WriterKind: types.WriterSyncer, WriterRef: "kubeservices", SourceID: src.ID}
	meshProv := types.Provenance{WriterKind: types.WriterSyncer, WriterRef: "mesh", SourceID: mesh.ID}

	ids, err := proj.UpsertEntities(ctx, srcProv, []EntityUpsert{
		{Kind: "application", IdentityKeys: map[string]string{"helm.release": "prod/a"}},
		{Kind: "application", IdentityKeys: map[string]string{"helm.release": "prod/b"}},
		{Kind: "service", IdentityKeys: map[string]string{"k8s.service": "prod/s"}},
		{Kind: "service", IdentityKeys: map[string]string{"k8s.service": "prod/s2"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, b, s, s2 := ids[0], ids[1], ids[2], ids[3]

	// src asserts A→S (will become stale), B→S (re-emitted), and A→S2.
	for _, e := range [][2]string{{a, s}, {b, s}, {a, s2}} {
		if err := proj.UpsertRelation(ctx, srcProv, "provides", e[0], e[1]); err != nil {
			t.Fatal(err)
		}
	}
	// The mesh CO-ASSERTS A→S2 (the same edge) — two presence rows on it.
	if err := proj.UpsertRelation(ctx, meshProv, "provides", a, s2); err != nil {
		t.Fatal(err)
	}
	if got := count(t, store, `SELECT count(*) FROM graph.relation WHERE type='provides'`); got != 3 {
		t.Fatalf("setup: 3 edges (A→S, B→S, A→S2), got %d", got)
	}
	if got := count(t, store, `SELECT count(*) FROM graph.relation_presence`); got != 4 {
		t.Fatalf("setup: 4 presence rows (src×3 + mesh×1), got %d", got)
	}

	// src's full sync now re-emits only B→S. Retract src presence for the rest;
	// delete edges whose LAST presence is gone.
	deleted, err := proj.RetractSourceRelationPresenceExcept(ctx, src.ID, "provides",
		[]string{b}, []string{s})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("only A→S (src-only, now presence-less) must be deleted, got %d", deleted)
	}

	// A→S gone (src was its only asserter). B→S survives (src still asserts).
	// A→S2 SURVIVES — the mesh still asserts it (union liveness).
	if got := count(t, store, `SELECT count(*) FROM graph.relation WHERE type='provides' AND from_id='`+a+`' AND to_id='`+s+`'`); got != 0 {
		t.Fatal("A→S must be collected (its last presence is gone)")
	}
	if got := count(t, store, `SELECT count(*) FROM graph.relation WHERE type='provides' AND from_id='`+b+`' AND to_id='`+s+`'`); got != 1 {
		t.Fatal("B→S must survive (src still asserts it)")
	}
	if got := count(t, store, `SELECT count(*) FROM graph.relation WHERE type='provides' AND from_id='`+a+`' AND to_id='`+s2+`'`); got != 1 {
		t.Fatal("A→S2 must survive — the mesh still asserts it (cross-source union liveness)")
	}
	// A→S2 now has exactly the mesh's presence (src's was retracted).
	if got := count(t, store, `
		SELECT count(*) FROM graph.relation_presence rp JOIN graph.relation r ON r.id=rp.relation_id
		WHERE r.from_id='`+a+`' AND r.to_id='`+s2+`'`); got != 1 {
		t.Fatalf("A→S2 must hold exactly the mesh presence, got %d", got)
	}

	// RelationTypesBySource still reflects the source's surviving edges.
	relTypes, err := store.RelationTypesBySource(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(relTypes) != 1 || relTypes[0] != "provides" {
		t.Fatalf("relation types by source: %v", relTypes)
	}
}

// TestRelationPresence_LastWriterIsOtherSource is the guardian MF-2 case: a Source
// that co-asserts an edge LAST-WRITTEN by another Source must still be found via
// relation_presence (not the edge's last-writer prov_source_id), so its full-sync
// sweep retracts its presence — else its phantom presence keeps the edge alive
// forever after every real asserter drops it.
func TestRelationPresence_LastWriterIsOtherSource(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	src, _ := store.RegisterSource(ctx, types.Source{Kind: "kubeservices", Name: "k8s"})
	mesh, _ := store.RegisterSource(ctx, types.Source{Kind: "mesh", Name: "istio"})
	proj := store.NormalizerProjector()
	ids, err := proj.UpsertEntities(ctx, types.Provenance{WriterKind: types.WriterSyncer, WriterRef: "k8s", SourceID: src.ID},
		[]EntityUpsert{
			{Kind: "service", IdentityKeys: map[string]string{"k8s.service": "prod/a"}},
			{Kind: "service", IdentityKeys: map[string]string{"k8s.service": "prod/b"}},
		})
	if err != nil {
		t.Fatal(err)
	}
	a, b := ids[0], ids[1]
	// src asserts A depends-on B FIRST; the mesh asserts the SAME edge LAST (so the
	// edge's prov_source_id = mesh). src has no other edges.
	if err := proj.UpsertRelation(ctx, types.Provenance{WriterKind: types.WriterSyncer, WriterRef: "k8s", SourceID: src.ID}, "depends-on", a, b); err != nil {
		t.Fatal(err)
	}
	if err := proj.UpsertRelation(ctx, types.Provenance{WriterKind: types.WriterSyncer, WriterRef: "mesh", SourceID: mesh.ID}, "depends-on", a, b); err != nil {
		t.Fatal(err)
	}

	// MF-2: the type is discoverable for src via PRESENCE, though the edge's
	// last-writer is the mesh.
	relTypes, err := store.RelationTypesBySource(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(relTypes) != 1 || relTypes[0] != "depends-on" {
		t.Fatalf("MF-2: src must find `depends-on` via presence despite mesh being last-writer, got %v", relTypes)
	}

	// src full-sync drops the edge: its presence retracted; edge survives on mesh.
	if _, err := proj.RetractSourceRelationPresenceExcept(ctx, src.ID, "depends-on", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := count(t, store, `SELECT count(*) FROM graph.relation WHERE type='depends-on'`); got != 1 {
		t.Fatal("the edge must survive — the mesh still asserts it")
	}
	// Now the mesh drops it too → last presence gone → edge collected.
	if _, err := proj.RetractSourceRelationPresenceExcept(ctx, mesh.ID, "depends-on", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := count(t, store, `SELECT count(*) FROM graph.relation WHERE type='depends-on'`); got != 0 {
		t.Fatal("with every asserter gone, the edge must be collected")
	}
}
