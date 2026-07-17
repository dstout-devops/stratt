package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/dstout-devops/stratt/types"
)

// Baselines (ADR-0019) are CaC-only: the desired-state engine is the sole
// writer, mirroring Triggers, Workflows, and Emitters.

// UpsertBaseline writes one declared Baseline.
func (s *Store) UpsertBaseline(ctx context.Context, b types.Baseline) error {
	spec, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("graph: marshal baseline spec: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO graph.baseline (name, spec)
		VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET spec = excluded.spec`,
		b.Name, spec)
	if err != nil {
		return fmt.Errorf("graph: upsert baseline: %w", err)
	}
	return nil
}

// GetBaseline returns one Baseline declaration.
func (s *Store) GetBaseline(ctx context.Context, name string) (types.Baseline, error) {
	var spec []byte
	err := s.pool.QueryRow(ctx,
		`SELECT spec FROM graph.baseline WHERE name = $1`, name).Scan(&spec)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.Baseline{}, fmt.Errorf("%w: baseline %s", ErrNotFound, name)
	}
	if err != nil {
		return types.Baseline{}, fmt.Errorf("graph: get baseline: %w", err)
	}
	var b types.Baseline
	if err := json.Unmarshal(spec, &b); err != nil {
		return b, fmt.Errorf("graph: decode baseline spec: %w", err)
	}
	return b, nil
}

// ListBaselines returns every Baseline declaration, ordered by name.
func (s *Store) ListBaselines(ctx context.Context) ([]types.Baseline, error) {
	rows, err := s.pool.Query(ctx, `SELECT spec FROM graph.baseline ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("graph: list baselines: %w", err)
	}
	defer rows.Close()
	var out []types.Baseline
	for rows.Next() {
		var spec []byte
		if err := rows.Scan(&spec); err != nil {
			return nil, fmt.Errorf("graph: list baselines: %w", err)
		}
		var b types.Baseline
		if err := json.Unmarshal(spec, &b); err != nil {
			return nil, fmt.Errorf("graph: decode baseline spec: %w", err)
		}
		if types.InScope(b.Environments, s.environment) { // env scope (ADR-0057)
			out = append(out, b)
		}
	}
	return out, rows.Err()
}

// DeleteBaseline removes one Baseline declaration.
func (s *Store) DeleteBaseline(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM graph.baseline WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("graph: delete baseline: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: baseline %s", ErrNotFound, name)
	}
	return nil
}
