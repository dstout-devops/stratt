package graph

import (
	"context"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// TestCellStore proves the graph.cell registry CRUD (mirror of Sites) and the
// <> 'local' CHECK that refuses the built-in Cell (ADR-0044).
func TestCellStore(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	c := types.Cell{Name: "us-east", Region: "us-east-1", Endpoint: "https://us-east.stratt.internal", Description: "primary", AuthzHome: true}
	if err := s.UpsertCell(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCell(ctx, "us-east")
	if err != nil {
		t.Fatal(err)
	}
	if got.Region != "us-east-1" || got.Endpoint != "https://us-east.stratt.internal" || got.DeclaredBy != "cac" || !got.AuthzHome {
		t.Fatalf("cell round-trip mismatch: %+v", got)
	}
	// Update.
	c.Description = "primary-updated"
	if err := s.UpsertCell(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCell(ctx, types.Cell{Name: "eu-west", Region: "eu-west-1", Endpoint: "https://eu-west.stratt.internal"}); err != nil {
		t.Fatal(err)
	}
	cells, err := s.ListCells(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != 2 || cells[0].Name != "eu-west" || cells[1].Name != "us-east" {
		t.Fatalf("ListCells must be ordered by name, got %+v", cells)
	}
	if err := s.DeleteCell(ctx, "eu-west"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCell(ctx, "eu-west"); err == nil {
		t.Fatal("deleted cell must not resolve")
	}

	// The built-in "local" Cell is never a row — the CHECK refuses it.
	if err := s.UpsertCell(ctx, types.Cell{Name: "local", Region: "r", Endpoint: "e"}); err == nil {
		t.Fatal("graph.cell must refuse the built-in 'local' Cell (mirror of graph.site)")
	}
}

// TestProvCellStamp proves writes stamp prov_cell from the Store's Cell id
// (ADR-0044): the default is 'local' (byte-identical to pre-Cells), and a named
// Cell stamps its name — "which Cell wrote this" has exactly one answer (§2.1).
func TestProvCellStamp(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	provCellOf := func(entityID string) string {
		t.Helper()
		var pc string
		if err := s.pool.QueryRow(ctx, `SELECT prov_cell FROM graph.entity WHERE id = $1`, entityID).Scan(&pc); err != nil {
			t.Fatalf("read prov_cell: %v", err)
		}
		return pc
	}

	// Default (Connect sets LocalCell).
	ids, err := s.NormalizerProjector().UpsertEntities(ctx, prov(types.WriterSyncer, "x/syncer"), []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"dns.fqdn": "a.example"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := provCellOf(ids[0]); got != types.LocalCell {
		t.Fatalf("default write must stamp prov_cell=local, got %q", got)
	}

	// Named Cell.
	s.SetCell("us-east")
	ids2, err := s.NormalizerProjector().UpsertEntities(ctx, prov(types.WriterSyncer, "x/syncer"), []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"dns.fqdn": "b.example"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := provCellOf(ids2[0]); got != "us-east" {
		t.Fatalf("named-Cell write must stamp prov_cell=us-east, got %q", got)
	}
	// A facet write on the same tx path stamps too.
	if err := s.RegisterFacetOwner(ctx, types.FacetOwner{Namespace: "os.kernel", OwnerKind: "syncer", OwnerRef: "x/syncer"}); err != nil {
		t.Fatal(err)
	}
	if err := s.NormalizerProjector().UpsertFacet(ctx, prov(types.WriterSyncer, "x/syncer"), ids2[0], "os.kernel", []byte(`{"family":"linux"}`)); err != nil {
		t.Fatal(err)
	}
	var fpc string
	if err := s.pool.QueryRow(ctx, `SELECT prov_cell FROM graph.facet WHERE entity_id = $1 AND namespace = 'os.kernel'`, ids2[0]).Scan(&fpc); err != nil {
		t.Fatal(err)
	}
	if fpc != "us-east" {
		t.Fatalf("facet write must stamp prov_cell=us-east, got %q", fpc)
	}
}

// TestSiteCell proves a Site round-trips its home Cell (Site → Cell, ADR-0044).
func TestSiteCell(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.UpsertSite(ctx, types.Site{Name: "edge-west", Mode: types.SiteModePush, Cell: "us-east"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSite(ctx, "edge-west")
	if err != nil {
		t.Fatal(err)
	}
	if got.Cell != "us-east" {
		t.Fatalf("Site must carry its home Cell, got %q", got.Cell)
	}
	// A Site with no declared Cell reads back empty (⇒ the built-in local Cell).
	if err := s.UpsertSite(ctx, types.Site{Name: "edge-local", Mode: types.SiteModePush}); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.GetSite(ctx, "edge-local")
	if got2.Cell != "" {
		t.Fatalf("undeclared Site Cell must be empty (⇒ local), got %q", got2.Cell)
	}
}
