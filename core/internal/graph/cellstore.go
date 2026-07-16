package graph

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/dstout-devops/stratt/types"
)

// Cells (ADR-0044) are CaC-declared control-plane shards: the desired-state
// engine is the sole writer, mirroring Site/View/Trigger/Emitter. Discrete
// columns rather than a spec blob — a Cell has a fixed, small shape. The
// built-in "local" Cell is never stored (the name <> 'local' CHECK refuses it),
// exactly as graph.site refuses "local".

// UpsertCell writes one declared Cell.
func (s *Store) UpsertCell(ctx context.Context, c types.Cell) error {
	declaredBy := c.DeclaredBy
	if declaredBy == "" {
		declaredBy = "cac"
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO graph.cell (name, region, endpoint, dispatch_prefix, description, declared_by, authz_home)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (name) DO UPDATE SET
			region = excluded.region, endpoint = excluded.endpoint,
			dispatch_prefix = excluded.dispatch_prefix, description = excluded.description,
			declared_by = excluded.declared_by, authz_home = excluded.authz_home`,
		c.Name, c.Region, c.Endpoint, c.DispatchPrefix, c.Description, declaredBy, c.AuthzHome)
	if err != nil {
		return fmt.Errorf("graph: upsert cell: %w", err)
	}
	return nil
}

// GetCell returns one Cell declaration.
func (s *Store) GetCell(ctx context.Context, name string) (types.Cell, error) {
	var c types.Cell
	var dispatchPrefix, description *string
	err := s.pool.QueryRow(ctx, `
		SELECT name, region, endpoint, dispatch_prefix, description, declared_by, authz_home
		FROM graph.cell WHERE name = $1`, name,
	).Scan(&c.Name, &c.Region, &c.Endpoint, &dispatchPrefix, &description, &c.DeclaredBy, &c.AuthzHome)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.Cell{}, fmt.Errorf("%w: cell %s", ErrNotFound, name)
	}
	if err != nil {
		return types.Cell{}, fmt.Errorf("graph: get cell: %w", err)
	}
	if dispatchPrefix != nil {
		c.DispatchPrefix = *dispatchPrefix
	}
	if description != nil {
		c.Description = *description
	}
	return c, nil
}

// ListCells returns every Cell declaration, ordered by name.
func (s *Store) ListCells(ctx context.Context) ([]types.Cell, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT name, region, endpoint, dispatch_prefix, description, declared_by, authz_home
		FROM graph.cell ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("graph: list cells: %w", err)
	}
	defer rows.Close()
	var out []types.Cell
	for rows.Next() {
		var c types.Cell
		var dispatchPrefix, description *string
		if err := rows.Scan(&c.Name, &c.Region, &c.Endpoint, &dispatchPrefix, &description, &c.DeclaredBy, &c.AuthzHome); err != nil {
			return nil, fmt.Errorf("graph: list cells: %w", err)
		}
		if dispatchPrefix != nil {
			c.DispatchPrefix = *dispatchPrefix
		}
		if description != nil {
			c.Description = *description
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Cell returns this Store's own Cell id (STRATT_CELL_ID, or LocalCell) — the
// residency home stamped on rows this daemon writes (ADR-0044).
func (s *Store) Cell() string { return s.projCell() }

// PeerCells returns the declared Cells OTHER than this one, ordered by name —
// the fan-out set for a cross-Cell Run (ADR-0044 slice 5). Empty ⇒ single-Cell
// estate (the no-op case): a Run then runs entirely local, byte-identically.
func (s *Store) PeerCells(ctx context.Context) ([]types.Cell, error) {
	all, err := s.ListCells(ctx)
	if err != nil {
		return nil, err
	}
	self := s.projCell()
	peers := make([]types.Cell, 0, len(all))
	for _, c := range all {
		if c.Name != self {
			peers = append(peers, c)
		}
	}
	return peers, nil
}

// DeleteCell removes one Cell declaration.
func (s *Store) DeleteCell(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM graph.cell WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("graph: delete cell: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: cell %s", ErrNotFound, name)
	}
	return nil
}
