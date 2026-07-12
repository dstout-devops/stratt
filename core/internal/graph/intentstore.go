package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/dstout-devops/stratt/types"
)

// Intents, Assignments, and Blueprints (ADR-0023) are CaC-only: the
// desired-state engine is the sole writer, mirroring the other declarables.

// UpsertIntent writes one declared Intent.
func (s *Store) UpsertIntent(ctx context.Context, in types.Intent) error {
	spec, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("graph: marshal intent spec: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO graph.intent (name, spec) VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET spec = excluded.spec`, in.Name, spec)
	if err != nil {
		return fmt.Errorf("graph: upsert intent: %w", err)
	}
	return nil
}

// GetIntent returns one Intent declaration.
func (s *Store) GetIntent(ctx context.Context, name string) (types.Intent, error) {
	var spec []byte
	err := s.pool.QueryRow(ctx, `SELECT spec FROM graph.intent WHERE name = $1`, name).Scan(&spec)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.Intent{}, fmt.Errorf("%w: intent %s", ErrNotFound, name)
	}
	if err != nil {
		return types.Intent{}, fmt.Errorf("graph: get intent: %w", err)
	}
	var in types.Intent
	if err := json.Unmarshal(spec, &in); err != nil {
		return in, fmt.Errorf("graph: decode intent spec: %w", err)
	}
	return in, nil
}

// ListIntents returns every Intent declaration, ordered by name.
func (s *Store) ListIntents(ctx context.Context) ([]types.Intent, error) {
	rows, err := s.pool.Query(ctx, `SELECT spec FROM graph.intent ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("graph: list intents: %w", err)
	}
	defer rows.Close()
	var out []types.Intent
	for rows.Next() {
		var spec []byte
		if err := rows.Scan(&spec); err != nil {
			return nil, fmt.Errorf("graph: list intents: %w", err)
		}
		var in types.Intent
		if err := json.Unmarshal(spec, &in); err != nil {
			return nil, fmt.Errorf("graph: decode intent spec: %w", err)
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// DeleteIntent removes one Intent declaration.
func (s *Store) DeleteIntent(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM graph.intent WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("graph: delete intent: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: intent %s", ErrNotFound, name)
	}
	return nil
}

// UpsertAssignment writes one declared Assignment.
func (s *Store) UpsertAssignment(ctx context.Context, a types.Assignment) error {
	spec, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("graph: marshal assignment spec: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO graph.assignment (name, spec) VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET spec = excluded.spec`, a.Name, spec)
	if err != nil {
		return fmt.Errorf("graph: upsert assignment: %w", err)
	}
	return nil
}

// GetAssignment returns one Assignment declaration.
func (s *Store) GetAssignment(ctx context.Context, name string) (types.Assignment, error) {
	var spec []byte
	err := s.pool.QueryRow(ctx, `SELECT spec FROM graph.assignment WHERE name = $1`, name).Scan(&spec)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.Assignment{}, fmt.Errorf("%w: assignment %s", ErrNotFound, name)
	}
	if err != nil {
		return types.Assignment{}, fmt.Errorf("graph: get assignment: %w", err)
	}
	var a types.Assignment
	if err := json.Unmarshal(spec, &a); err != nil {
		return a, fmt.Errorf("graph: decode assignment spec: %w", err)
	}
	return a, nil
}

// ListAssignments returns every Assignment declaration, ordered by name.
func (s *Store) ListAssignments(ctx context.Context) ([]types.Assignment, error) {
	rows, err := s.pool.Query(ctx, `SELECT spec FROM graph.assignment ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("graph: list assignments: %w", err)
	}
	defer rows.Close()
	var out []types.Assignment
	for rows.Next() {
		var spec []byte
		if err := rows.Scan(&spec); err != nil {
			return nil, fmt.Errorf("graph: list assignments: %w", err)
		}
		var a types.Assignment
		if err := json.Unmarshal(spec, &a); err != nil {
			return nil, fmt.Errorf("graph: decode assignment spec: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteAssignment removes one Assignment declaration.
func (s *Store) DeleteAssignment(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM graph.assignment WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("graph: delete assignment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: assignment %s", ErrNotFound, name)
	}
	return nil
}
