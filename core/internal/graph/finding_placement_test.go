package graph

import (
	"context"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// TestCellPlacementFindings proves the §2.4 authority rule (ADR-0044): an Entity
// whose home Cell disagrees with a Source observing it (a cross-Cell identity
// collision — the multi-master condition) raises a placement Finding, which
// resolves when the collision clears. Single-Cell writes nothing.
func TestCellPlacementFindings(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// A fresh projector is taken after each SetCell — the projector captures the
	// Store's Cell at construction (in production SetCell runs once at startup,
	// before any projector exists).

	// Two Sources homed in different Cells (Source.cell = registering daemon's).
	s.SetCell("cell-a")
	srcA := mustSource(t, s, "chef", "acme-chef")
	provA := syncerProv("chef/syncer", srcA)
	s.SetCell("cell-b")
	srcB := mustSource(t, s, "puppet", "acme-puppet")
	provB := syncerProv("puppet/syncer", srcB)

	// Entity created by cell-a's daemon (home_cell=cell-a, presence srcA)…
	s.SetCell("cell-a")
	ids, err := s.NormalizerProjector().UpsertEntities(ctx, provA, []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"chef.node.name": "h1", "dns.fqdn": "f1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	eid := ids[0]
	// …then observed by cell-b's daemon via a shared identity → correlate-UPDATE
	// (home stays cell-a), adding presence for srcB (homed in cell-b).
	s.SetCell("cell-b")
	if _, err := s.NormalizerProjector().UpsertEntities(ctx, provB, []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"puppet.certname": "h1", "dns.fqdn": "f1"}},
	}); err != nil {
		t.Fatal(err)
	}

	// The collision surfaces as exactly one placement Finding.
	n, err := s.WriteCellPlacementFindings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("a cross-Cell collision must raise exactly 1 placement Finding, got %d", n)
	}
	f := findingByTarget(t, s, "__placement__", "entity:"+eid)
	if f.Framework != "placement" || f.Severity != "critical" || f.Status != types.FindingOpen {
		t.Fatalf("placement Finding shape: framework=%q severity=%q status=%q", f.Framework, f.Severity, f.Status)
	}
	// Idempotent — a second sweep leaves exactly one live placement Finding.
	if _, err := s.WriteCellPlacementFindings(ctx); err != nil {
		t.Fatal(err)
	}
	if openPlacement := countPlacementOpen(t, s); openPlacement != 1 {
		t.Fatalf("placement sweep must be idempotent, got %d open placement findings", openPlacement)
	}

	// Clear the collision: cell-b's Source stops observing the host.
	if _, err := s.NormalizerProjector().TombstoneByIdentity(ctx, provB, "puppet.certname", "h1"); err != nil {
		t.Fatal(err)
	}
	m, err := s.ResolveClearedCellPlacementFindings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m != 1 {
		t.Fatalf("reconciled collision must resolve 1 placement Finding, got %d", m)
	}
	resolved := findingByTarget(t, s, "__placement__", "entity:"+eid)
	if resolved.Status != types.FindingResolved || resolved.ResolvedReason != "placement-reconciled" {
		t.Fatalf("resolved placement Finding: status=%q reason=%q", resolved.Status, resolved.ResolvedReason)
	}
	if m2, _ := s.ResolveClearedCellPlacementFindings(ctx); m2 != 0 {
		t.Fatalf("second resolve must be a no-op, got %d", m2)
	}
}

// TestCellPlacementSingleCellNoop proves the placement sweep is a provable no-op
// for a single-Cell 'local' estate (the backward-compat guarantee).
func TestCellPlacementSingleCellNoop(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	chef := syncerProv("chef/syncer", mustSource(t, s, "chef", "acme-chef"))
	if _, err := s.NormalizerProjector().UpsertEntities(ctx, chef, []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"chef.node.name": "h1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.WriteCellPlacementFindings(ctx); err != nil || n != 0 {
		t.Fatalf("single-Cell estate must write 0 placement findings, got %d err=%v", n, err)
	}
}

func countPlacementOpen(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM graph.finding WHERE framework = 'placement' AND status <> 'resolved'`).Scan(&n); err != nil {
		t.Fatalf("count placement: %v", err)
	}
	return n
}
