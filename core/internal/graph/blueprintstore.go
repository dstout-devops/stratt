package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/dstout-devops/stratt/types"
)

// Blueprints (ADR-0023) are CaC-only and versioned: (name, version) is the
// identity so an upgrade rolls through rings alongside the old version.

// UpsertBlueprint writes one declared Blueprint version.
func (s *Store) UpsertBlueprint(ctx context.Context, b types.Blueprint) error {
	spec, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("graph: marshal blueprint spec: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO graph.blueprint (name, version, spec) VALUES ($1, $2, $3)
		ON CONFLICT (name, version) DO UPDATE SET spec = excluded.spec`,
		b.Name, b.Version, spec)
	if err != nil {
		return fmt.Errorf("graph: upsert blueprint: %w", err)
	}
	return nil
}

// GetBlueprint returns one Blueprint version.
func (s *Store) GetBlueprint(ctx context.Context, name string, version int) (types.Blueprint, error) {
	var spec []byte
	err := s.pool.QueryRow(ctx,
		`SELECT spec FROM graph.blueprint WHERE name = $1 AND version = $2`, name, version).Scan(&spec)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.Blueprint{}, fmt.Errorf("%w: blueprint %s@%d", ErrNotFound, name, version)
	}
	if err != nil {
		return types.Blueprint{}, fmt.Errorf("graph: get blueprint: %w", err)
	}
	var b types.Blueprint
	if err := json.Unmarshal(spec, &b); err != nil {
		return b, fmt.Errorf("graph: decode blueprint spec: %w", err)
	}
	return b, nil
}

// ListBlueprints returns every Blueprint version, ordered by name+version.
func (s *Store) ListBlueprints(ctx context.Context) ([]types.Blueprint, error) {
	rows, err := s.pool.Query(ctx, `SELECT spec FROM graph.blueprint ORDER BY name, version`)
	if err != nil {
		return nil, fmt.Errorf("graph: list blueprints: %w", err)
	}
	defer rows.Close()
	var out []types.Blueprint
	for rows.Next() {
		var spec []byte
		if err := rows.Scan(&spec); err != nil {
			return nil, fmt.Errorf("graph: list blueprints: %w", err)
		}
		var b types.Blueprint
		if err := json.Unmarshal(spec, &b); err != nil {
			return nil, fmt.Errorf("graph: decode blueprint spec: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// DeleteBlueprint removes one Blueprint version.
func (s *Store) DeleteBlueprint(ctx context.Context, name string, version int) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM graph.blueprint WHERE name = $1 AND version = $2`, name, version)
	if err != nil {
		return fmt.Errorf("graph: delete blueprint: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: blueprint %s@%d", ErrNotFound, name, version)
	}
	return nil
}

// ── assignment membership (membership-delta + max-delta, §4.3) ──────────────

// AssignmentMembership is the stored compiled-membership snapshot for one
// Assignment — what the next compile diffs against.
type AssignmentMembership struct {
	Assignment  string
	EntityIDs   []string
	MemberCount int
	AckedDelta  int
}

// GetAssignmentMembership returns the stored snapshot; ok=false when none
// exists yet (first compile of the Assignment).
func (s *Store) GetAssignmentMembership(ctx context.Context, assignment string) (AssignmentMembership, bool, error) {
	var m AssignmentMembership
	m.Assignment = assignment
	err := s.pool.QueryRow(ctx, `
		SELECT entity_ids, member_count, acked_delta
		FROM graph.assignment_membership WHERE assignment = $1`, assignment,
	).Scan(&m.EntityIDs, &m.MemberCount, &m.AckedDelta)
	if errors.Is(err, pgx.ErrNoRows) {
		return AssignmentMembership{Assignment: assignment}, false, nil
	}
	if err != nil {
		return m, false, fmt.Errorf("graph: get assignment membership: %w", err)
	}
	return m, true, nil
}

// PutAssignmentMembership writes the compiled-membership snapshot.
func (s *Store) PutAssignmentMembership(ctx context.Context, m AssignmentMembership) error {
	if m.EntityIDs == nil {
		m.EntityIDs = []string{}
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO graph.assignment_membership (assignment, entity_ids, member_count, acked_delta, compiled_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (assignment) DO UPDATE
		SET entity_ids = excluded.entity_ids, member_count = excluded.member_count,
		    acked_delta = excluded.acked_delta, compiled_at = now()`,
		m.Assignment, m.EntityIDs, m.MemberCount, m.AckedDelta)
	if err != nil {
		return fmt.Errorf("graph: put assignment membership: %w", err)
	}
	return nil
}

// DeleteAssignmentMembership drops one Assignment's snapshot (on withdrawal).
func (s *Store) DeleteAssignmentMembership(ctx context.Context, assignment string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM graph.assignment_membership WHERE assignment = $1`, assignment)
	if err != nil {
		return fmt.Errorf("graph: delete assignment membership: %w", err)
	}
	return nil
}
