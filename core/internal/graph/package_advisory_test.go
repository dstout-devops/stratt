package graph

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func TestVersionLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"3.0.5", "3.0.7", true},
		{"3.0.7", "3.0.7", false},
		{"3.0.8", "3.0.7", false},
		{"1.2", "1.2.1", true},
		{"2.0", "1.9.9", false},
		{"1.10", "1.9", false}, // numeric, not lexical: 10 > 9
	}
	for _, c := range cases {
		if got := versionLess(c.a, c.b); got != c.want {
			t.Errorf("versionLess(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}

// TestCheckPackageAdvisories proves ADR-0080 slice 1: the deliverable-software
// dimension (software.package) turns into patch/vulnerability remediation signal —
// an advisory raises a Finding for a host running an affected version, and none for
// a patched one. DB-gated.
func TestCheckPackageAdvisories(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	// Register the software.package owner (a collector) and project a host inventory.
	if err := store.RegisterFacetOwner(ctx, types.FacetOwner{
		Namespace: "software.package", OwnerKind: string(types.WriterSyncer), OwnerRef: "pkg-collector",
	}); err != nil {
		t.Fatal(err)
	}
	src, err := store.RegisterSource(ctx, types.Source{Kind: "collector", Name: "pkg"})
	if err != nil {
		t.Fatal(err)
	}
	inv, _ := json.Marshal(map[string]any{"packages": []map[string]any{
		{"name": "openssl", "version": "3.0.5", "origin": "distro", "deliveryForm": "package"},
		{"name": "curl", "version": "8.5.0", "origin": "distro", "deliveryForm": "package"},
	}})
	proj := store.NormalizerProjector()
	prov := types.Provenance{WriterKind: types.WriterSyncer, WriterRef: "pkg-collector", SourceID: src.ID}
	if _, err := proj.UpsertEntities(ctx, prov, []EntityUpsert{{
		Kind:         "host",
		IdentityKeys: map[string]string{"dns.fqdn": "web01.corp"},
		Facets:       map[string]json.RawMessage{"software.package": inv},
	}}); err != nil {
		t.Fatalf("project inventory: %v", err)
	}

	advisories := []types.PackageAdvisory{
		{ID: "CVE-2022-3602", Package: "openssl", Fixed: "3.0.7", Severity: "high", Title: "X.509 buffer overflow"},
		{ID: "CVE-9999-0000", Package: "curl", Fixed: "8.0.0", Severity: "high", Title: "not applicable — curl is patched"},
	}
	if err := store.CheckPackageAdvisories(ctx, advisories); err != nil {
		t.Fatalf("check: %v", err)
	}

	// Exactly one Finding: openssl 3.0.5 < 3.0.7. curl 8.5.0 >= 8.0.0 → none.
	if got := count(t, store, `SELECT count(*) FROM graph.finding WHERE framework='patch/advisory' AND status='open'`); got != 1 {
		t.Fatalf("want 1 open patch/advisory Finding, got %d", got)
	}

	// Idempotent: re-run converges (no duplicate Finding).
	if err := store.CheckPackageAdvisories(ctx, advisories); err != nil {
		t.Fatalf("re-check: %v", err)
	}
	if got := count(t, store, `SELECT count(*) FROM graph.finding WHERE framework='patch/advisory' AND status='open'`); got != 1 {
		t.Fatalf("after re-check want 1 Finding, got %d", got)
	}

	// INV (MF-1): the check is READ-ONLY — it must never author inventory. The
	// software.package facet is byte-identical after the check (projection
	// discipline enforced, not asserted). Remediation flows to the SoR, not here.
	var after []byte
	if err := store.pool.QueryRow(ctx, `
		SELECT f.value FROM graph.facet f JOIN graph.entity e ON e.id=f.entity_id
		WHERE e.kind='host' AND f.namespace='software.package'`).Scan(&after); err != nil {
		t.Fatalf("read inventory after: %v", err)
	}
	var got, want map[string]any
	_ = json.Unmarshal(after, &got)
	_ = json.Unmarshal(inv, &want)
	if gb, _ := json.Marshal(got); string(gb) != string(mustMarshal(want)) {
		t.Fatalf("the check mutated the inventory facet — it must be read-only")
	}
}

// TestCheckPackageAdvisories_Unassessable proves §1.8 (F-1): a version the
// comparator cannot rank (epoch/tilde) is surfaced as a Finding, never silently
// treated as safe — no silent false-negative on a security advisory.
func TestCheckPackageAdvisories_Unassessable(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.RegisterFacetOwner(ctx, types.FacetOwner{
		Namespace: "software.package", OwnerKind: string(types.WriterSyncer), OwnerRef: "pkg-collector",
	}); err != nil {
		t.Fatal(err)
	}
	src, _ := store.RegisterSource(ctx, types.Source{Kind: "collector", Name: "pkg"})
	inv, _ := json.Marshal(map[string]any{"packages": []map[string]any{
		{"name": "openssl", "version": "1:3.0.5"}, // Debian epoch — comparator can't rank it
	}})
	proj := store.NormalizerProjector()
	prov := types.Provenance{WriterKind: types.WriterSyncer, WriterRef: "pkg-collector", SourceID: src.ID}
	if _, err := proj.UpsertEntities(ctx, prov, []EntityUpsert{{
		Kind: "host", IdentityKeys: map[string]string{"dns.fqdn": "db01.corp"},
		Facets: map[string]json.RawMessage{"software.package": inv},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckPackageAdvisories(ctx, []types.PackageAdvisory{
		{ID: "CVE-2022-3602", Package: "openssl", Fixed: "3.0.7", Severity: "high"},
	}); err != nil {
		t.Fatalf("check: %v", err)
	}
	// It must NOT silently pass: an unassessable Finding is raised for triage.
	if got := count(t, store, `SELECT count(*) FROM graph.finding WHERE framework='patch/advisory' AND status='open'`); got != 1 {
		t.Fatalf("an unrankable version must surface a Finding (never silent-safe), got %d", got)
	}
}

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
