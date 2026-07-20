// Package graph is the graph-store frontend (charter §3): Postgres-backed
// Entities, Relations, Facets, Provenance, Views, Sources, and Run summaries.
//
// The graph is a rebuildable projection, never a second truth (§1.2). The
// write surface is deliberately split:
//
//   - Projector — the ONLY type that can write Entities/Facets/Relations. It
//     stamps every row with Provenance and declares its write path
//     (normalizer or run-provenance) as a transaction-local Postgres setting;
//     triggers installed by the migrations reject any write that arrives
//     without it. Bypassing Projector therefore fails at the data layer, not
//     in review.
//   - Store — reads, plus the non-projection registries (Views, Sources, Run
//     summaries, the facet-ownership registry), which are not Entity
//     attributes and carry their own rules.
package graph

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dstout-devops/stratt/types"
)

// Store is the graph-store frontend over a pgx pool.
type Store struct {
	pool *pgxpool.Pool
	// cell is this control-plane Cell's id (ADR-0044), stamped as prov_cell on
	// every write. Defaults to LocalCell so a single-Cell deployment is
	// unchanged; main.go sets it from STRATT_CELL_ID.
	cell string
	// environment is this daemon's active logical environment (ADR-0057), a
	// DIFFERENT axis from cell (Cell = physical residency; environment = logical
	// dev/staging/prod slice within a Cell). Empty = UNSCOPED: the daemon sees
	// every declaration (byte-identical to pre-ADR-0057). When set, the cac list
	// reads below return only in-scope rows, so out-of-scope declarations are
	// invisible to the prune candidate set and the compiler (the §1.2 data-layer
	// fix — never a "own substrate" convention). main.go sets it from
	// STRATT_ENVIRONMENT.
	environment string
}

// SetCell sets the Cell id stamped into write provenance (ADR-0044). Called
// once at startup from STRATT_CELL_ID; empty is treated as LocalCell.
func (s *Store) SetCell(cell string) {
	if cell == "" {
		cell = types.LocalCell
	}
	s.cell = cell
}

// SetEnvironment sets this daemon's active environment (ADR-0057). Called once at
// startup from STRATT_ENVIRONMENT; empty leaves the daemon unscoped (sees all).
func (s *Store) SetEnvironment(env string) { s.environment = env }

// ActiveEnvironment returns this daemon's active environment ("" = unscoped).
// The reconcile Controller reads it to filter the apply-set consistently with
// the store-scoped list reads.
func (s *Store) ActiveEnvironment() string { return s.environment }

// New wraps an existing pool. The caller owns the pool's lifecycle.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Connect opens a pool, verifies connectivity, and runs pending migrations.
func Connect(ctx context.Context, databaseURL string) (*Store, error) {
	s, err := ConnectNoMigrate(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := Migrate(ctx, s.pool); err != nil {
		s.pool.Close()
		return nil, err
	}
	return s, nil
}

// ConnectNoMigrate connects + pings but does NOT run migrations. It is for
// serving replicas during a rolling upgrade where a Helm pre-upgrade Job owns the
// schema change (UPG-1, ADR-0078): N replicas never race Up(), and a migration is
// applied once, in a controlled step, under the expand/contract rule (each
// release's schema stays compatible with the previous release's code). The
// migrate-only Job uses MigrateURL; the serving Deployment uses this.
func ConnectNoMigrate(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("graph: open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("graph: ping: %w", err)
	}
	return &Store{pool: pool, cell: types.LocalCell}, nil
}

// Close releases the underlying pool (only when created via Connect).
func (s *Store) Close() { s.pool.Close() }

// Ping verifies the database is reachable — the readiness signal (ADR-0040).
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }
