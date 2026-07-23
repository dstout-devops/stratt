package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/dstout-devops/stratt/types"
)

// Actuators (charter §2.3, ADR-0103) are CaC-only: every row is a projection of the Git
// declaration, so there is no declared_by column and no API write path — the desired-state
// engine is the sole writer. Distinct Named Kind from Connector: no Source, no ownership.

// UpsertActuator writes one declared Actuator.
func (s *Store) UpsertActuator(ctx context.Context, a types.Actuator) error {
	spec, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("graph: marshal actuator spec: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO graph.actuator (name, spec)
		VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET spec = excluded.spec`,
		a.Name, spec)
	if err != nil {
		return fmt.Errorf("graph: upsert actuator: %w", err)
	}
	return nil
}

// GetActuator returns one Actuator declaration.
func (s *Store) GetActuator(ctx context.Context, name string) (types.Actuator, error) {
	var spec []byte
	err := s.pool.QueryRow(ctx,
		`SELECT spec FROM graph.actuator WHERE name = $1`, name).Scan(&spec)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.Actuator{}, fmt.Errorf("%w: actuator %s", ErrNotFound, name)
	}
	if err != nil {
		return types.Actuator{}, fmt.Errorf("graph: get actuator: %w", err)
	}
	var a types.Actuator
	if err := json.Unmarshal(spec, &a); err != nil {
		return a, fmt.Errorf("graph: decode actuator spec: %w", err)
	}
	return a, nil
}

// ListActuators returns every Actuator declaration in the active environment, by name.
func (s *Store) ListActuators(ctx context.Context) ([]types.Actuator, error) {
	rows, err := s.pool.Query(ctx, `SELECT spec FROM graph.actuator ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("graph: list actuators: %w", err)
	}
	defer rows.Close()
	var out []types.Actuator
	for rows.Next() {
		var spec []byte
		if err := rows.Scan(&spec); err != nil {
			return nil, fmt.Errorf("graph: list actuators: %w", err)
		}
		var a types.Actuator
		if err := json.Unmarshal(spec, &a); err != nil {
			return nil, fmt.Errorf("graph: decode actuator spec: %w", err)
		}
		if types.InScope(a.Environments, s.environment) { // env scope (ADR-0057)
			out = append(out, a)
		}
	}
	return out, rows.Err()
}

// DeleteActuator removes one Actuator declaration.
func (s *Store) DeleteActuator(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM graph.actuator WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("graph: delete actuator: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: actuator %s", ErrNotFound, name)
	}
	return nil
}
