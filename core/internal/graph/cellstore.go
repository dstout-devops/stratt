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
		INSERT INTO graph.cell (name, region, endpoint, dispatch_prefix, description, declared_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (name) DO UPDATE SET
			region = excluded.region, endpoint = excluded.endpoint,
			dispatch_prefix = excluded.dispatch_prefix, description = excluded.description,
			declared_by = excluded.declared_by`,
		c.Name, c.Region, c.Endpoint, c.DispatchPrefix, c.Description, declaredBy)
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
		SELECT name, region, endpoint, dispatch_prefix, description, declared_by
		FROM graph.cell WHERE name = $1`, name,
	).Scan(&c.Name, &c.Region, &c.Endpoint, &dispatchPrefix, &description, &c.DeclaredBy)
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
		SELECT name, region, endpoint, dispatch_prefix, description, declared_by
		FROM graph.cell ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("graph: list cells: %w", err)
	}
	defer rows.Close()
	var out []types.Cell
	for rows.Next() {
		var c types.Cell
		var dispatchPrefix, description *string
		if err := rows.Scan(&c.Name, &c.Region, &c.Endpoint, &dispatchPrefix, &description, &c.DeclaredBy); err != nil {
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
