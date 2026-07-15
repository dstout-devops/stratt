package graph

import (
	"context"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// TestHomeCellSetOnce proves residency (home_cell) is set once at creation and
// NOT overwritten when a different Cell's daemon re-observes the Entity, while
// prov_cell (last-writer) flips — the invariant that makes a cross-Cell stray
// write observable (ADR-0044 slice 2).
func TestHomeCellSetOnce(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	cellsOf := func(id string) (home, prov string) {
		t.Helper()
		if err := s.pool.QueryRow(ctx, `SELECT home_cell, prov_cell FROM graph.entity WHERE id = $1`, id).Scan(&home, &prov); err != nil {
			t.Fatalf("read cells: %v", err)
		}
		return
	}

	s.SetCell("cell-east")
	ids, err := s.NormalizerProjector().UpsertEntities(ctx, prov(types.WriterSyncer, "x/syncer"), []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"dns.fqdn": "a.example"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	eid := ids[0]
	if home, pc := cellsOf(eid); home != "cell-east" || pc != "cell-east" {
		t.Fatalf("at creation home_cell and prov_cell = creating Cell, got home=%q prov=%q", home, pc)
	}

	// A different Cell's daemon re-observes the same identity → correlate-UPDATE.
	s.SetCell("cell-west")
	if _, err := s.NormalizerProjector().UpsertEntities(ctx, prov(types.WriterSyncer, "x/syncer"), []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"dns.fqdn": "a.example"}},
	}); err != nil {
		t.Fatal(err)
	}
	if home, pc := cellsOf(eid); home != "cell-east" || pc != "cell-west" {
		t.Fatalf("residency must be stable (home stays creating Cell) while prov flips, got home=%q prov=%q", home, pc)
	}
}

// TestSourceCell proves a Source homes to the registering daemon's Cell
// (ADR-0044), round-tripping through GetSource.
func TestSourceCell(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Default Cell (Connect sets LocalCell).
	src, err := s.RegisterSource(ctx, types.Source{Kind: "vcenter", Name: "vc-local", Endpoint: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if src.Cell != types.LocalCell {
		t.Fatalf("Source registered by a local daemon must home to local, got %q", src.Cell)
	}
	if got, _ := s.GetSource(ctx, "vc-local"); got.Cell != types.LocalCell {
		t.Fatalf("GetSource cell round-trip: got %q", got.Cell)
	}

	// Named Cell.
	s.SetCell("cell-east")
	src2, err := s.RegisterSource(ctx, types.Source{Kind: "vcenter", Name: "vc-east", Endpoint: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if src2.Cell != "cell-east" {
		t.Fatalf("Source registered by cell-east daemon must home to cell-east, got %q", src2.Cell)
	}
	if got, _ := s.GetSource(ctx, "vc-east"); got.Cell != "cell-east" {
		t.Fatalf("GetSource cell round-trip (named): got %q", got.Cell)
	}
}

// TestRunCell proves a Run homes to the launching daemon's Cell and the Cells
// union round-trips (ADR-0044).
func TestRunCell(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	run, err := s.CreateRun(ctx, types.Run{WorkflowID: "wf-local"})
	if err != nil {
		t.Fatal(err)
	}
	if run.Cell != types.LocalCell {
		t.Fatalf("Run launched by a local daemon must home to local, got %q", run.Cell)
	}

	s.SetCell("cell-east")
	run2, err := s.CreateRun(ctx, types.Run{WorkflowID: "wf-east"})
	if err != nil {
		t.Fatal(err)
	}
	if run2.Cell != "cell-east" {
		t.Fatalf("Run homes to launching Cell, got %q", run2.Cell)
	}
	if err := s.SetRunCells(ctx, run2.ID, []string{"cell-east", "cell-west"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRun(ctx, run2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cell != "cell-east" || len(got.Cells) != 2 || got.Cells[0] != "cell-east" || got.Cells[1] != "cell-west" {
		t.Fatalf("GetRun cell/cells round-trip: cell=%q cells=%v", got.Cell, got.Cells)
	}
	// nil SetRunCells is a no-op.
	if err := s.SetRunCells(ctx, run2.ID, nil); err != nil {
		t.Fatalf("nil SetRunCells must be a no-op: %v", err)
	}
}
