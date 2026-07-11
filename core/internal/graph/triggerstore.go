package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/dstout-devops/stratt/types"
)

// Triggers (charter §2, ADR-0010) are CaC-only in v1: every row is a
// projection of the Git declaration, so there is no declared_by column and
// no API write path — the desired-state engine is the sole writer.

// UpsertTrigger writes one declared Trigger.
func (s *Store) UpsertTrigger(ctx context.Context, t types.Trigger) error {
	spec, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("graph: marshal trigger spec: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO graph.trigger (name, kind, spec)
		VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET kind = excluded.kind, spec = excluded.spec`,
		t.Name, t.Kind, spec)
	if err != nil {
		return fmt.Errorf("graph: upsert trigger: %w", err)
	}
	return nil
}

// GetTrigger returns one Trigger declaration.
func (s *Store) GetTrigger(ctx context.Context, name string) (types.Trigger, error) {
	var spec []byte
	err := s.pool.QueryRow(ctx,
		`SELECT spec FROM graph.trigger WHERE name = $1`, name).Scan(&spec)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.Trigger{}, fmt.Errorf("%w: trigger %s", ErrNotFound, name)
	}
	if err != nil {
		return types.Trigger{}, fmt.Errorf("graph: get trigger: %w", err)
	}
	var t types.Trigger
	if err := json.Unmarshal(spec, &t); err != nil {
		return t, fmt.Errorf("graph: decode trigger spec: %w", err)
	}
	return t, nil
}

// ListTriggers returns every Trigger declaration, ordered by name.
func (s *Store) ListTriggers(ctx context.Context) ([]types.Trigger, error) {
	rows, err := s.pool.Query(ctx, `SELECT spec FROM graph.trigger ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("graph: list triggers: %w", err)
	}
	defer rows.Close()
	var out []types.Trigger
	for rows.Next() {
		var spec []byte
		if err := rows.Scan(&spec); err != nil {
			return nil, fmt.Errorf("graph: list triggers: %w", err)
		}
		var t types.Trigger
		if err := json.Unmarshal(spec, &t); err != nil {
			return nil, fmt.Errorf("graph: decode trigger spec: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteTrigger removes one Trigger declaration.
func (s *Store) DeleteTrigger(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM graph.trigger WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("graph: delete trigger: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: trigger %s", ErrNotFound, name)
	}
	return nil
}
