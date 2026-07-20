package graph

import (
	"context"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// TestServiceINV1_WritePathEnforced proves ADR-0081 INV-1 is STRUCTURAL, not
// convention: a write to a service.* facet OR a `provides` edge that bypasses the
// projector's write-path (a raw insert with no stratt.write_path declared — i.e. not
// a Normalizer/Run projection) is REJECTED by the data-layer triggers. This is what
// keeps the service dimension a read-model projection, never a writable service
// catalog (the writable-CMDB non-goal).
func TestServiceINV1_WritePathEnforced(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	// A legitimately-projected service Entity (through the write-path).
	if err := store.RegisterFacetOwner(ctx, types.FacetOwner{
		Namespace: "service.endpoint", OwnerKind: string(types.WriterSyncer), OwnerRef: "kubeservices",
	}); err != nil {
		t.Fatal(err)
	}
	src, _ := store.RegisterSource(ctx, types.Source{Kind: "kubeservices", Name: "k8s"})
	prov := types.Provenance{WriterKind: types.WriterSyncer, WriterRef: "kubeservices", SourceID: src.ID}
	ids, err := store.NormalizerProjector().UpsertEntities(ctx, prov, []EntityUpsert{
		{Kind: "service", IdentityKeys: map[string]string{"k8s.service": "prod/web"}},
		{Kind: "application", IdentityKeys: map[string]string{"helm.release": "prod/shop"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	svcID, appID := ids[0], ids[1]

	// A RAW facet write, bypassing the projector (no stratt.write_path GUC) — must be
	// rejected by enforce_write_path.
	_, err = store.pool.Exec(ctx, `
		INSERT INTO graph.facet (entity_id, namespace, value, prov_writer_kind, prov_writer_ref, prov_source_id, prov_cell, prov_at)
		VALUES ($1, 'service.endpoint', '{"ports":[]}', 'syncer', 'attacker', $2, 'local', now())`,
		svcID, src.ID)
	if err == nil {
		t.Fatal("INV-1: a raw service.endpoint write outside the write-path must be rejected")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "write") && !strings.Contains(strings.ToLower(err.Error()), "path") {
		t.Fatalf("INV-1 facet: expected a write-path rejection, got: %v", err)
	}

	// A RAW `provides` edge write, bypassing the projector — must be rejected by
	// relation_write_path.
	_, err = store.pool.Exec(ctx, `
		INSERT INTO graph.relation (type, from_id, to_id, prov_writer_kind, prov_writer_ref, prov_source_id, prov_cell, prov_at)
		VALUES ('provides', $1, $2, 'syncer', 'attacker', $3, 'local', now())`,
		appID, svcID, src.ID)
	if err == nil {
		t.Fatal("INV-1: a raw `provides` edge write outside the write-path must be rejected")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "write") && !strings.Contains(strings.ToLower(err.Error()), "path") {
		t.Fatalf("INV-1 relation: expected a write-path rejection, got: %v", err)
	}
}
