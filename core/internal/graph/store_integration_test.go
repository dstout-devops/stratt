package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	neturl "net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dstout-devops/stratt/types"
)

// testStore connects to the dev-substrate Postgres (task dev:up), runs the
// migrations into a throwaway database, and returns a Store on it. Skips when
// no database is reachable so `go test ./...` stays green pre-substrate.
func testStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("STRATT_TEST_DATABASE_URL")
	if url == "" {
		url = "postgres://stratt:stratt-dev@localhost:5432/stratt"
	}
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("no test database reachable (%v) — run `task dev:up`", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Skipf("no test database reachable (%v) — run `task dev:up`", err)
	}

	name := fmt.Sprintf("stratt_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
		admin.Close()
	})

	u, err := neturl.Parse(url)
	if err != nil {
		t.Fatalf("parse database url: %v", err)
	}
	u.Path = "/" + name
	store, err := Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("connect+migrate test database: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

func prov(kind types.WriterKind, ref string) types.Provenance {
	return types.Provenance{WriterKind: kind, WriterRef: ref, SourceID: "src-test", At: time.Now().UTC()}
}

// TestWritePathEnforcement proves charter §1.2 is a data-layer property: a
// plain INSERT outside the Projector is rejected by the database itself.
func TestWritePathEnforcement(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	_, err := s.pool.Exec(ctx, `
		INSERT INTO graph.entity (kind, prov_writer_kind, prov_writer_ref, prov_at)
		VALUES ('vm', 'syncer', 'rogue', now())`)
	if err == nil {
		t.Fatal("direct INSERT into graph.entity succeeded — §1.2 write-path enforcement is broken")
	}
	if !strings.Contains(err.Error(), "§1.2") {
		t.Fatalf("rejection should cite the charter discipline, got: %v", err)
	}
}

// TestFacetOwnership proves the ownership registry (§2.1): unregistered
// namespaces reject writes; a non-owner Syncer is rejected; the owner and
// Run-provenance writes (§4.3) succeed.
func TestFacetOwnership(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := s.NormalizerProjector()

	ids, err := p.UpsertEntities(ctx, prov(types.WriterSyncer, "vcenter/syncer"), []EntityUpsert{
		{Kind: "vm", IdentityKeys: map[string]string{"vcenter.uuid": "u-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := ids[0]

	// Unregistered namespace → rejected.
	err = p.UpsertFacet(ctx, prov(types.WriterSyncer, "vcenter/syncer"), id, "os.kernel", json.RawMessage(`{"family":"linux"}`))
	if err == nil || !strings.Contains(err.Error(), "no registered owner") {
		t.Fatalf("write to unregistered namespace should fail with registration error, got: %v", err)
	}

	if err := s.RegisterFacetOwner(ctx, types.FacetOwner{Namespace: "os.kernel", OwnerKind: "syncer", OwnerRef: "vcenter/syncer"}); err != nil {
		t.Fatal(err)
	}

	// Double-claim by a different owner → registration error (§2.1).
	err = s.RegisterFacetOwner(ctx, types.FacetOwner{Namespace: "os.kernel", OwnerKind: "syncer", OwnerRef: "other/syncer"})
	if !errors.Is(err, ErrOwnerConflict) {
		t.Fatalf("second owner registration should be ErrOwnerConflict, got: %v", err)
	}

	// Owner writes → ok.
	if err := p.UpsertFacet(ctx, prov(types.WriterSyncer, "vcenter/syncer"), id, "os.kernel", json.RawMessage(`{"family":"linux"}`)); err != nil {
		t.Fatal(err)
	}

	// A different Syncer writing the owned namespace → rejected.
	err = p.UpsertFacet(ctx, prov(types.WriterSyncer, "other/syncer"), id, "os.kernel", json.RawMessage(`{"family":"bsd"}`))
	if err == nil || !strings.Contains(err.Error(), "owned by") {
		t.Fatalf("non-owner syncer write should be rejected, got: %v", err)
	}

	// Run provenance writes ahead of Syncer lag (§4.3) → ok.
	rp := s.RunProjector()
	if err := rp.UpsertFacet(ctx, prov(types.WriterRun, "run-123"), id, "os.kernel", json.RawMessage(`{"family":"linux","release":"6.6"}`)); err != nil {
		t.Fatalf("run-provenance write should be admissible (§4.3): %v", err)
	}

	// Versioning: two successful writes → version 2, history has both.
	facets, err := s.GetFacets(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(facets) != 1 || facets[0].Provenance.WriterRef != "run-123" {
		t.Fatalf("facet provenance should show the last writer, got %+v", facets)
	}
	var histCount int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM graph.facet_history WHERE entity_id = $1 AND namespace = 'os.kernel'`, id,
	).Scan(&histCount); err != nil {
		t.Fatal(err)
	}
	if histCount != 2 {
		t.Fatalf("facet_history should hold every version, got %d rows", histCount)
	}
}

// TestIdentityCorrelation proves observations correlate onto one Entity via
// identity keys, and conflicts surface instead of merging silently.
func TestIdentityCorrelation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := s.NormalizerProjector()
	pv := prov(types.WriterSyncer, "vcenter/syncer")

	ids1, err := p.UpsertEntities(ctx, pv, []EntityUpsert{
		{Kind: "vm", IdentityKeys: map[string]string{"vcenter.uuid": "u-1"}, Labels: map[string]string{"env": "dev"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Same identity, new scheme → same Entity, updated labels.
	ids2, err := p.UpsertEntities(ctx, pv, []EntityUpsert{
		{Kind: "vm", IdentityKeys: map[string]string{"vcenter.uuid": "u-1", "dns.fqdn": "a.example"}, Labels: map[string]string{"env": "prod"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ids1[0] != ids2[0] {
		t.Fatalf("same identity key should correlate to one Entity: %s vs %s", ids1[0], ids2[0])
	}
	e, err := s.GetEntity(ctx, ids1[0])
	if err != nil {
		t.Fatal(err)
	}
	if e.Labels["env"] != "prod" || e.IdentityKeys["dns.fqdn"] != "a.example" {
		t.Fatalf("entity should carry updated labels and merged identities, got %+v", e)
	}

	// A second Entity, then an observation matching both → conflict surfaces.
	if _, err := p.UpsertEntities(ctx, pv, []EntityUpsert{
		{Kind: "vm", IdentityKeys: map[string]string{"vcenter.uuid": "u-2"}},
	}); err != nil {
		t.Fatal(err)
	}
	_, err = p.UpsertEntities(ctx, pv, []EntityUpsert{
		{Kind: "vm", IdentityKeys: map[string]string{"vcenter.uuid": "u-2", "dns.fqdn": "a.example"}},
	})
	if !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("ambiguous correlation should be ErrIdentityConflict, got: %v", err)
	}
}

// TestViewResolution proves Views resolve their live Entity set and version
// on change, and tombstoned Entities leave the set.
func TestViewResolution(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := s.NormalizerProjector()
	pv := prov(types.WriterSyncer, "vcenter/syncer")

	if err := s.RegisterFacetOwner(ctx, types.FacetOwner{Namespace: "os.kernel", OwnerKind: "syncer", OwnerRef: "vcenter/syncer"}); err != nil {
		t.Fatal(err)
	}
	var batch []EntityUpsert
	for i := range 10 {
		labels := map[string]string{"env": "dev"}
		if i < 3 {
			labels["env"] = "prod"
		}
		batch = append(batch, EntityUpsert{
			Kind:         "vm",
			IdentityKeys: map[string]string{"vcenter.uuid": fmt.Sprintf("u-%d", i)},
			Labels:       labels,
			Facets:       map[string]json.RawMessage{"os.kernel": json.RawMessage(`{"family":"linux"}`)},
		})
	}
	if _, err := p.UpsertEntities(ctx, pv, batch); err != nil {
		t.Fatal(err)
	}

	v, err := s.DeclareView(ctx, "test/prod-linux", types.ViewSelector{
		Kinds:  []string{"vm"},
		Labels: map[string]string{"env": "prod"},
		Facets: []types.FacetPredicate{{Namespace: "os.kernel", Path: "family", Equals: json.RawMessage(`"linux"`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if v.Version != 1 {
		t.Fatalf("fresh view should be version 1, got %d", v.Version)
	}

	_, ents, err := s.ResolveView(ctx, "test/prod-linux", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 3 {
		t.Fatalf("view should resolve 3 prod entities, got %d", len(ents))
	}

	// Redeclare with a changed selector → version bumps, history records.
	v2, err := s.DeclareView(ctx, "test/prod-linux", types.ViewSelector{Kinds: []string{"vm"}})
	if err != nil {
		t.Fatal(err)
	}
	if v2.Version != 2 {
		t.Fatalf("changed view should be version 2, got %d", v2.Version)
	}

	// Redeclaring the same selector is a no-op (the Git sync controller
	// re-declares every reconcile in Phase 1) — no version churn.
	v3, err := s.DeclareView(ctx, "test/prod-linux", types.ViewSelector{Kinds: []string{"vm"}})
	if err != nil {
		t.Fatal(err)
	}
	if v3.Version != 2 {
		t.Fatalf("unchanged redeclare should stay version 2, got %d", v3.Version)
	}

	// Tombstone: only u-0..u-4 still seen → 5 entities leave the live set.
	n, err := p.TombstoneAbsent(ctx, pv, "vcenter.uuid", []string{"u-0", "u-1", "u-2", "u-3", "u-4"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("expected 5 tombstones, got %d", n)
	}
	count, err := s.CountSelector(ctx, types.ViewSelector{Kinds: []string{"vm"}})
	if err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Fatalf("live set after tombstoning should be 5, got %d", count)
	}
}

// TestRunSummaries covers the Run summary lifecycle (§2.3: summaries only).
func TestRunSummaries(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	r, err := s.CreateRun(ctx, types.Run{WorkflowID: "wf-1", ViewRef: "view://test/prod-linux", ViewVersion: 1, TriggeredBy: "nightly-facts"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunStatus(ctx, r.ID, types.RunRunning, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunStatus(ctx, r.ID, types.RunSucceeded, map[string]any{"ok": 12, "failed": 0}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRun(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != types.RunSucceeded || got.FinishedAt == nil {
		t.Fatalf("terminal run should be succeeded with finished_at, got %+v", got)
	}
	if got.TriggeredBy != "nightly-facts" {
		t.Fatalf("triggered_by should round-trip, got %+v", got)
	}
}

// TestWorkflowRunsAndGates covers the ADR-0011 execution records: the
// WorkflowRun lifecycle, Gate open/decide (incl. the pending-only guard),
// and the Workflow → Run descent queries.
func TestWorkflowRunsAndGates(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	wr, err := s.CreateWorkflowRun(ctx, "patch", "", "alice", "probe-trigger")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorkflowRunTemporalID(ctx, wr.ID, "wfrun-"+wr.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorkflowRunStatus(ctx, wr.ID, types.RunRunning, nil); err != nil {
		t.Fatal(err)
	}

	// A Step Run linked to the execution.
	r, err := s.CreateRun(ctx, types.Run{WorkflowID: "wfrun-x-gather", ViewRef: "view://all", ViewVersion: 1, WorkflowRunID: wr.ID, StepName: "gather"})
	if err != nil {
		t.Fatal(err)
	}
	runs, err := s.ListRunsForWorkflowRun(ctx, wr.ID)
	if err != nil || len(runs) != 1 || runs[0].StepName != "gather" || runs[0].ID != r.ID {
		t.Fatalf("descent runs: %v %v", runs, err)
	}
	got, err := s.GetRun(ctx, r.ID)
	if err != nil || got.WorkflowRunID != wr.ID || got.StepName != "gather" {
		t.Fatalf("run linkage round-trip: %+v %v", got, err)
	}

	// Gate lifecycle: open (idempotent), decide once, refuse re-decision.
	approvers := types.GateApprovers{Teams: []string{"platform"}}
	g, err := s.CreateGate(ctx, wr.ID, "approve", approvers)
	if err != nil {
		t.Fatal(err)
	}
	g2, err := s.CreateGate(ctx, wr.ID, "approve", approvers) // activity retry
	if err != nil || g2.ID != g.ID {
		t.Fatalf("gate create must be idempotent per (run, step): %v %v", g2, err)
	}
	pending, err := s.ListGates(ctx, types.GatePending)
	if err != nil || len(pending) != 1 || pending[0].Approvers.Teams[0] != "platform" {
		t.Fatalf("pending inbox: %v %v", pending, err)
	}
	if err := s.DecideGate(ctx, g.ID, types.GateApproved, "bob", "lgtm"); err != nil {
		t.Fatal(err)
	}
	if err := s.DecideGate(ctx, g.ID, types.GateApproved, "bob", "lgtm"); err != nil {
		t.Fatalf("same decision retry must be a noop: %v", err)
	}
	if err := s.DecideGate(ctx, g.ID, types.GateDenied, "eve", "no"); !errors.Is(err, ErrConflict) {
		t.Fatalf("re-decision must conflict, got %v", err)
	}
	gates, err := s.ListGatesForWorkflowRun(ctx, wr.ID)
	if err != nil || len(gates) != 1 || gates[0].Status != types.GateApproved || gates[0].DecidedBy != "bob" {
		t.Fatalf("decided gate: %v %v", gates, err)
	}

	if err := s.SetWorkflowRunStatus(ctx, wr.ID, types.RunSucceeded, map[string]any{"steps": map[string]any{"gather": "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	final, summary, err := s.GetWorkflowRun(ctx, wr.ID)
	if err != nil || final.Status != types.RunSucceeded || final.FinishedAt == nil || final.Principal != "alice" || final.TriggeredBy != "probe-trigger" {
		t.Fatalf("terminal workflow run: %+v %v", final, err)
	}
	if _, ok := summary["steps"]; !ok {
		t.Fatalf("summary round-trip: %v", summary)
	}
}

// TestContractPinning covers ADR-0015: registration is idempotent per
// name+version+hash, and drift against a pin is blocking.
func TestContractPinning(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	c := types.Contract{
		Name: "actuators/test.input", Version: 1, Rung: "hand-written",
		Hash: "aaaa", Schema: []byte(`{"type":"object"}`),
	}
	if err := s.RegisterContract(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterContract(ctx, c); err != nil {
		t.Fatalf("same pin must be a noop: %v", err)
	}
	// Same name+version, different bytes → blocking drift naming both hashes.
	drifted := c
	drifted.Hash = "bbbb"
	err := s.RegisterContract(ctx, drifted)
	if !errors.Is(err, ErrContractDrift) || !strings.Contains(err.Error(), "aaaa") || !strings.Contains(err.Error(), "bbbb") {
		t.Fatalf("drift must block naming both hashes, got %v", err)
	}
	// A new version coexists.
	v2 := drifted
	v2.Version = 2
	if err := s.RegisterContract(ctx, v2); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListContracts(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("list: %v %v", all, err)
	}
}

// TestFacetSchemaEnforcement proves the pinned os.kernel schema rejects
// malformed writes at the write path itself (§1.5) — for the Run-provenance
// projector too, and for uncovered namespaces nothing changes.
func TestFacetSchemaEnforcement(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := s.NormalizerProjector()
	pv := prov(types.WriterSyncer, "vcenter/syncer")

	if err := s.RegisterFacetOwner(ctx, types.FacetOwner{Namespace: "os.kernel", OwnerKind: "syncer", OwnerRef: "vcenter/syncer"}); err != nil {
		t.Fatal(err)
	}
	ids, err := p.UpsertEntities(ctx, pv, []EntityUpsert{{Kind: "vm", IdentityKeys: map[string]string{"vcenter.uuid": "u-cx"}}})
	if err != nil {
		t.Fatal(err)
	}
	// Valid document → accepted.
	if err := p.UpsertFacet(ctx, pv, ids[0], "os.kernel", json.RawMessage(`{"family":"linux","release":"6.6"}`)); err != nil {
		t.Fatalf("valid os.kernel rejected: %v", err)
	}
	// Off-schema property → rejected with the contract named.
	err = p.UpsertFacet(ctx, pv, ids[0], "os.kernel", json.RawMessage(`{"family":"linux","kernelParams":["quiet"]}`))
	if err == nil || !strings.Contains(err.Error(), "facets/os.kernel") {
		t.Fatalf("off-schema os.kernel must be rejected naming the contract, got %v", err)
	}
	// Uncovered namespace (no demanded schema, §1.1) → passes as before.
	if err := s.RegisterFacetOwner(ctx, types.FacetOwner{Namespace: "vm.config", OwnerKind: "syncer", OwnerRef: "vcenter/syncer"}); err != nil {
		t.Fatal(err)
	}
	if err := p.UpsertFacet(ctx, pv, ids[0], "vm.config", json.RawMessage(`{"anything":true}`)); err != nil {
		t.Fatalf("uncovered namespace must not be validated: %v", err)
	}
}

// TestDerivedContractVersioning covers ADR-0017: derived documents
// auto-version on hash change instead of blocking like shipped pins.
func TestDerivedContractVersioning(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	v, err := s.RegisterDerivedContract(ctx, "opentofu/ws.outputs", "tool-derived", "h1", []byte(`{"a":1}`))
	if err != nil || v != 1 {
		t.Fatalf("first: v=%d err=%v", v, err)
	}
	v, err = s.RegisterDerivedContract(ctx, "opentofu/ws.outputs", "tool-derived", "h1", []byte(`{"a":1}`))
	if err != nil || v != 1 {
		t.Fatalf("same hash must be a noop at v1: v=%d err=%v", v, err)
	}
	v, err = s.RegisterDerivedContract(ctx, "opentofu/ws.outputs", "tool-derived", "h2", []byte(`{"a":2}`))
	if err != nil || v != 2 {
		t.Fatalf("new hash must auto-version: v=%d err=%v", v, err)
	}
	// Shipped (rung-1) semantics unchanged: same name+version, new hash blocks.
	if err := s.RegisterContract(ctx, types.Contract{Name: "opentofu/ws.outputs", Version: 2, Rung: "tool-derived", Hash: "h3", Schema: []byte(`{}`)}); !errors.Is(err, ErrContractDrift) {
		t.Fatalf("pin-path drift must still block: %v", err)
	}
}
