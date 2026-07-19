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

// TestCheckSoftwareAdvisories_CrossForm proves ADR-0080 slice 3: ONE form-agnostic
// check covers the whole software dimension. A single advisory ruleset fires on a
// vulnerable installed PACKAGE and a vulnerable CONTAINER image alike, and leaves
// patched components alone. DB-gated.
func TestCheckSoftwareAdvisories_CrossForm(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	// Register both form owners (§2.1 one owner per namespace) and a source.
	for _, ns := range []string{"software.package", "software.container"} {
		if err := store.RegisterFacetOwner(ctx, types.FacetOwner{
			Namespace: ns, OwnerKind: string(types.WriterSyncer), OwnerRef: ns + "-collector",
		}); err != nil {
			t.Fatal(err)
		}
	}
	src, err := store.RegisterSource(ctx, types.Source{Kind: "collector", Name: "sw"})
	if err != nil {
		t.Fatal(err)
	}
	proj := store.NormalizerProjector()

	// A host with a vulnerable openssl PACKAGE and a patched curl.
	pkgs, _ := json.Marshal(map[string]any{"packages": []map[string]any{
		{"name": "openssl", "version": "3.0.5"},
		{"name": "curl", "version": "8.5.0"},
	}})
	provPkg := types.Provenance{WriterKind: types.WriterSyncer, WriterRef: "software.package-collector", SourceID: src.ID}
	if _, err := proj.UpsertEntities(ctx, provPkg, []EntityUpsert{{
		Kind: "host", IdentityKeys: map[string]string{"dns.fqdn": "web01.corp"},
		Facets: map[string]json.RawMessage{"software.package": pkgs},
	}}); err != nil {
		t.Fatalf("project packages: %v", err)
	}

	// A pod with a vulnerable log4j-app CONTAINER image and a patched sidecar.
	ctrs, _ := json.Marshal(map[string]any{"containers": []map[string]any{
		{"name": "log4j-app", "version": "2.14", "digest": "sha256:abc"},
		{"name": "envoy", "version": "1.30"},
	}})
	provCtr := types.Provenance{WriterKind: types.WriterSyncer, WriterRef: "software.container-collector", SourceID: src.ID}
	if _, err := proj.UpsertEntities(ctx, provCtr, []EntityUpsert{{
		Kind: "host", IdentityKeys: map[string]string{"dns.fqdn": "pod-xyz.cluster"},
		Facets: map[string]json.RawMessage{"software.container": ctrs},
	}}); err != nil {
		t.Fatalf("project containers: %v", err)
	}

	advisories := []types.SoftwareAdvisory{
		{ID: "CVE-2022-3602", Component: "openssl", Fixed: "3.0.7", Severity: "high"},   // package hit
		{ID: "CVE-2021-44228", Component: "log4j-app", Fixed: "2.17", Severity: "high"}, // container hit
		{ID: "CVE-NOPE", Component: "curl", Fixed: "8.0.0", Severity: "high"},           // patched: no hit
	}
	if err := store.CheckSoftwareAdvisories(ctx, advisories); err != nil {
		t.Fatalf("check: %v", err)
	}

	// Exactly two Findings — one per form. curl (patched) and envoy (no advisory) none.
	if got := count(t, store, `SELECT count(*) FROM graph.finding WHERE framework='patch/advisory' AND status='open'`); got != 2 {
		t.Fatalf("want 2 cross-form Findings (package + container), got %d", got)
	}

	// Idempotent: re-run converges.
	if err := store.CheckSoftwareAdvisories(ctx, advisories); err != nil {
		t.Fatalf("re-check: %v", err)
	}
	if got := count(t, store, `SELECT count(*) FROM graph.finding WHERE framework='patch/advisory' AND status='open'`); got != 2 {
		t.Fatalf("after re-check want 2 Findings, got %d", got)
	}

	// INV: the check is READ-ONLY — the software.package inventory is untouched.
	var after []byte
	if err := store.pool.QueryRow(ctx, `
		SELECT f.value FROM graph.facet f JOIN graph.entity e ON e.id=f.entity_id
		WHERE f.namespace='software.package'`).Scan(&after); err != nil {
		t.Fatalf("read inventory after: %v", err)
	}
	var got, want map[string]any
	_ = json.Unmarshal(after, &got)
	_ = json.Unmarshal(pkgs, &want)
	if gb, _ := json.Marshal(got); string(gb) != string(mustMarshal(want)) {
		t.Fatal("the check mutated the inventory facet — it must be read-only")
	}
}

// TestCheckSoftwareAdvisories_Unassessable proves §1.8: a version the comparator
// cannot rank (an epoch package version, a non-numeric image tag) surfaces a
// Finding, never a silent false-negative on a security advisory.
func TestCheckSoftwareAdvisories_Unassessable(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.RegisterFacetOwner(ctx, types.FacetOwner{
		Namespace: "software.container", OwnerKind: string(types.WriterSyncer), OwnerRef: "c",
	}); err != nil {
		t.Fatal(err)
	}
	src, _ := store.RegisterSource(ctx, types.Source{Kind: "collector", Name: "sw"})
	// A container tagged "latest" — the comparator cannot rank it.
	ctrs, _ := json.Marshal(map[string]any{"containers": []map[string]any{
		{"name": "log4j-app", "version": "latest"},
	}})
	proj := store.NormalizerProjector()
	prov := types.Provenance{WriterKind: types.WriterSyncer, WriterRef: "c", SourceID: src.ID}
	if _, err := proj.UpsertEntities(ctx, prov, []EntityUpsert{{
		Kind: "host", IdentityKeys: map[string]string{"dns.fqdn": "pod-1.cluster"},
		Facets: map[string]json.RawMessage{"software.container": ctrs},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSoftwareAdvisories(ctx, []types.SoftwareAdvisory{
		{ID: "CVE-2021-44228", Component: "log4j-app", Fixed: "2.17", Severity: "high"},
	}); err != nil {
		t.Fatalf("check: %v", err)
	}
	if got := count(t, store, `SELECT count(*) FROM graph.finding WHERE framework='patch/advisory' AND status='open'`); got != 1 {
		t.Fatalf("an unrankable tag must surface a Finding (never silent-safe), got %d", got)
	}
}

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
