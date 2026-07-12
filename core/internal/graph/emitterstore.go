package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/dstout-devops/stratt/types"
)

// Emitters (ADR-0018) are CaC-only: the desired-state engine is the sole
// writer, mirroring Triggers and Workflows.

// UpsertEmitter writes one declared Emitter.
func (s *Store) UpsertEmitter(ctx context.Context, e types.Emitter) error {
	spec, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("graph: marshal emitter spec: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO graph.emitter (name, kind, spec)
		VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET kind = excluded.kind, spec = excluded.spec`,
		e.Name, e.Kind, spec)
	if err != nil {
		return fmt.Errorf("graph: upsert emitter: %w", err)
	}
	return nil
}

// GetEmitter returns one Emitter declaration.
func (s *Store) GetEmitter(ctx context.Context, name string) (types.Emitter, error) {
	var spec []byte
	err := s.pool.QueryRow(ctx,
		`SELECT spec FROM graph.emitter WHERE name = $1`, name).Scan(&spec)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.Emitter{}, fmt.Errorf("%w: emitter %s", ErrNotFound, name)
	}
	if err != nil {
		return types.Emitter{}, fmt.Errorf("graph: get emitter: %w", err)
	}
	var e types.Emitter
	if err := json.Unmarshal(spec, &e); err != nil {
		return e, fmt.Errorf("graph: decode emitter spec: %w", err)
	}
	return e, nil
}

// ListEmitters returns every Emitter declaration, ordered by name.
func (s *Store) ListEmitters(ctx context.Context) ([]types.Emitter, error) {
	rows, err := s.pool.Query(ctx, `SELECT spec FROM graph.emitter ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("graph: list emitters: %w", err)
	}
	defer rows.Close()
	var out []types.Emitter
	for rows.Next() {
		var spec []byte
		if err := rows.Scan(&spec); err != nil {
			return nil, fmt.Errorf("graph: list emitters: %w", err)
		}
		var e types.Emitter
		if err := json.Unmarshal(spec, &e); err != nil {
			return nil, fmt.Errorf("graph: decode emitter spec: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeleteEmitter removes one Emitter declaration.
func (s *Store) DeleteEmitter(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM graph.emitter WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("graph: delete emitter: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: emitter %s", ErrNotFound, name)
	}
	return nil
}
