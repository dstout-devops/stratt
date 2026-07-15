package graph

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// certBaseline is a damping-immediate baseline for the tombstone-GC tests.
func certBaseline() types.Baseline {
	return types.Baseline{
		Name: "cert-expiry", ViewName: "certs", Cron: "@hourly",
		Severity: types.SeverityWarning, Framework: "intent", DampingObservations: 1,
	}
}

// openFindingOnEntity projects a cert Entity (identity cert.serial=serial) and
// opens a Finding on it via a damping-immediate observation. Returns the Entity
// id and the Finding id.
func openFindingOnEntity(t *testing.T, s *Store, prov types.Provenance, serial, target string) (string, string) {
	t.Helper()
	ctx := context.Background()
	ids, err := s.NormalizerProjector().UpsertEntities(ctx, prov, []EntityUpsert{
		{Kind: "cert", IdentityKeys: map[string]string{"cert.serial": serial}},
	})
	if err != nil {
		t.Fatalf("upsert cert: %v", err)
	}
	eid := ids[0]
	run, err := s.CreateRun(ctx, types.Run{WorkflowID: "wf-cert", Baseline: "cert-expiry"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	out, err := s.RecordBaselineObservations(ctx, certBaseline(), run.ID, map[string]BaselineObservation{
		target: {Drifted: true, EntityID: eid, Detail: json.RawMessage(`[{"expires":"soon"}]`)},
	})
	if err != nil || out.Opened != 1 {
		t.Fatalf("open finding: out=%+v err=%v", out, err)
	}
	return eid, findingByTarget(t, s, "cert-expiry", target).ID
}

func findingByTarget(t *testing.T, s *Store, baseline, target string) types.Finding {
	t.Helper()
	fs, err := s.ListFindings(context.Background(), baseline, "", 0)
	if err != nil {
		t.Fatalf("list findings: %v", err)
	}
	for _, f := range fs {
		if f.Target == target {
			return f
		}
	}
	t.Fatalf("finding for target %s not found", target)
	return types.Finding{}
}

// TestResolveFindingsForTombstonedEntities is the ADR-0043 renewal happy path
// (T1) plus the non-tombstoned guard (T3): tombstoning a cert Entity resolves
// its open Finding with reason entity-tombstoned, keeping the audit row; a live
// Entity's Finding stays open.
func TestResolveFindingsForTombstonedEntities(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	certs := syncerProv("certissuer/syncer", mustSource(t, s, "certissuer", "acme-certs"))

	_, oldFinding := openFindingOnEntity(t, s, certs, "serialA", "serialA")
	// A second cert stays live — its Finding must NOT be swept (T3).
	_, liveFindingID := openFindingOnEntity(t, s, certs, "serialB", "serialB")

	// Renewal: serialA is no longer reported → tombstoned (single-source).
	if _, err := s.NormalizerProjector().TombstoneAbsent(ctx, certs, "cert.serial", []string{"serialB"}); err != nil {
		t.Fatal(err)
	}

	n, err := s.ResolveFindingsForTombstonedEntities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("exactly the tombstoned cert's Finding must resolve, got %d", n)
	}
	got, err := s.GetFinding(ctx, oldFinding)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != types.FindingResolved || got.ResolvedReason != "entity-tombstoned" || got.ResolvedAt == nil {
		t.Fatalf("old cert Finding must resolve with reason entity-tombstoned, got status=%s reason=%q at=%v", got.Status, got.ResolvedReason, got.ResolvedAt)
	}
	live, err := s.GetFinding(ctx, liveFindingID)
	if err != nil {
		t.Fatal(err)
	}
	if live.Status != types.FindingOpen {
		t.Fatalf("live cert's Finding must stay open, got %s", live.Status)
	}
}

// TestSweepSkipsCoManagedAndEntityless proves the sweep only touches genuinely
// tombstoned Entities: a co-managed host that stays live (T2), and orphan /
// workspace (entity-less) Findings (T4).
func TestSweepSkipsCoManagedAndEntityless(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := s.NormalizerProjector()

	chef := syncerProv("chef/syncer", mustSource(t, s, "chef", "acme-chef"))
	puppet := syncerProv("puppet/syncer", mustSource(t, s, "puppet", "acme-puppet"))

	// Co-managed host, correlated via dns.fqdn.
	ids, err := p.UpsertEntities(ctx, chef, []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"chef.node.name": "h1", "dns.fqdn": "f1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	eid := ids[0]
	if _, err := p.UpsertEntities(ctx, puppet, []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"puppet.certname": "h1", "dns.fqdn": "f1"}},
	}); err != nil {
		t.Fatal(err)
	}
	run, _ := s.CreateRun(ctx, types.Run{WorkflowID: "wf-h", Baseline: "cert-expiry"})
	if _, err := s.RecordBaselineObservations(ctx, certBaseline(), run.ID, map[string]BaselineObservation{
		"host-drift": {Drifted: true, EntityID: eid, Detail: json.RawMessage(`[{"x":1}]`)},
	}); err != nil {
		t.Fatal(err)
	}
	// An orphan Finding (entity-less) and a workspace Finding (entity-less).
	if err := s.WriteOrphanFinding(ctx, "cert-expiry", "assignment:foo", "warning", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordBaselineObservations(ctx, certBaseline(), run.ID, map[string]BaselineObservation{
		"workspace-x": {Drifted: true, Detail: json.RawMessage(`[{"y":2}]`)}, // no EntityID
	}); err != nil {
		t.Fatal(err)
	}

	// Chef drops the host — but Puppet still observes it, so it stays LIVE.
	if _, err := p.TombstoneAbsent(ctx, chef, "chef.node.name", []string{}); err != nil {
		t.Fatal(err)
	}

	n, err := s.ResolveFindingsForTombstonedEntities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("nothing must resolve: co-managed host is live, orphan/workspace are entity-less; got %d", n)
	}
	if f := findingByTarget(t, s, "cert-expiry", "host-drift"); f.Status != types.FindingOpen {
		t.Fatalf("co-managed host Finding must stay open, got %s", f.Status)
	}
	if f := findingByTarget(t, s, "cert-expiry", "assignment:foo"); f.Status != types.FindingOpen {
		t.Fatalf("orphan Finding must stay open, got %s", f.Status)
	}
	if f := findingByTarget(t, s, "cert-expiry", "workspace-x"); f.Status != types.FindingOpen {
		t.Fatalf("workspace Finding must stay open, got %s", f.Status)
	}
}

// TestSweepIdempotentAndSelfHealing proves the sweep is a no-op the second time
// and heals a Finding opened AFTER the tombstone (T5, the race a
// transition-triggered write cannot catch).
func TestSweepIdempotentAndSelfHealing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	certs := syncerProv("certissuer/syncer", mustSource(t, s, "certissuer", "acme-certs"))

	eid, fid := openFindingOnEntity(t, s, certs, "serialA", "serialA")
	// Tombstone FIRST, then run the sweep — the Finding was opened before the
	// tombstone; a later-opened Finding on an already-tombstoned Entity is the
	// race guard below.
	if _, err := s.NormalizerProjector().TombstoneAbsent(ctx, certs, "cert.serial", []string{}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.ResolveFindingsForTombstonedEntities(ctx); n != 1 {
		t.Fatalf("first sweep must resolve 1, got %d", n)
	}
	if n, _ := s.ResolveFindingsForTombstonedEntities(ctx); n != 0 {
		t.Fatalf("second sweep must be a no-op, got %d", n)
	}
	_ = fid

	// Race guard: open a NEW Finding directly on the already-tombstoned Entity.
	run, _ := s.CreateRun(ctx, types.Run{WorkflowID: "wf-race", Baseline: "cert-expiry"})
	if _, err := s.RecordBaselineObservations(ctx, certBaseline(), run.ID, map[string]BaselineObservation{
		"late": {Drifted: true, EntityID: eid, Detail: json.RawMessage(`[{"z":3}]`)},
	}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.ResolveFindingsForTombstonedEntities(ctx); n != 1 {
		t.Fatalf("sweep must heal a Finding opened after the tombstone, got %d", n)
	}
	if f := findingByTarget(t, s, "cert-expiry", "late"); f.Status != types.FindingResolved || f.ResolvedReason != "entity-tombstoned" {
		t.Fatalf("late Finding must resolve entity-tombstoned, got status=%s reason=%q", f.Status, f.ResolvedReason)
	}
}

// TestCleanResolveStampsReason proves the normal clean-observation resolve
// stamps observed-clean (T7 symmetry), distinguishing it from a tombstone GC.
func TestCleanResolveStampsReason(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	b := certBaseline()
	run, _ := s.CreateRun(ctx, types.Run{WorkflowID: "wf-clean", Baseline: "cert-expiry"})

	if _, err := s.RecordBaselineObservations(ctx, b, run.ID, map[string]BaselineObservation{
		"t1": {Drifted: true, EntityID: "ent-clean", Detail: json.RawMessage(`[{"a":1}]`)},
	}); err != nil {
		t.Fatal(err)
	}
	// A clean observation resolves it.
	if _, err := s.RecordBaselineObservations(ctx, b, run.ID, map[string]BaselineObservation{
		"t1": {Drifted: false},
	}); err != nil {
		t.Fatal(err)
	}
	f := findingByTarget(t, s, "cert-expiry", "t1")
	if f.Status != types.FindingResolved || f.ResolvedReason != "observed-clean" {
		t.Fatalf("clean resolve must stamp observed-clean, got status=%s reason=%q", f.Status, f.ResolvedReason)
	}
}
