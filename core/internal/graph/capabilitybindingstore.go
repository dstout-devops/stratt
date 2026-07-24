package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/dstout-devops/stratt/types"
)

// CapabilityBindings (ADR-0110 D3) are CaC-only: every row is a projection of the Git declaration,
// so there is no declared_by column and no API write path — the desired-state engine is the sole
// writer. NOT a Named Kind (§2 frozen); a declaration form the capability registry reconciles.

// UpsertCapabilityBinding writes one declared CapabilityBinding.
func (s *Store) UpsertCapabilityBinding(ctx context.Context, b types.CapabilityBinding) error {
	spec, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("graph: marshal capability-binding spec: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO graph.capability_binding (name, spec)
		VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET spec = excluded.spec`,
		b.Name, spec)
	if err != nil {
		return fmt.Errorf("graph: upsert capability-binding: %w", err)
	}
	return nil
}

// GetCapabilityBinding returns one CapabilityBinding declaration.
func (s *Store) GetCapabilityBinding(ctx context.Context, name string) (types.CapabilityBinding, error) {
	var spec []byte
	err := s.pool.QueryRow(ctx,
		`SELECT spec FROM graph.capability_binding WHERE name = $1`, name).Scan(&spec)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.CapabilityBinding{}, fmt.Errorf("%w: capability-binding %s", ErrNotFound, name)
	}
	if err != nil {
		return types.CapabilityBinding{}, fmt.Errorf("graph: get capability-binding: %w", err)
	}
	var b types.CapabilityBinding
	if err := json.Unmarshal(spec, &b); err != nil {
		return b, fmt.Errorf("graph: decode capability-binding spec: %w", err)
	}
	return b, nil
}

// ListCapabilityBindings returns every CapabilityBinding in the active environment, by name.
func (s *Store) ListCapabilityBindings(ctx context.Context) ([]types.CapabilityBinding, error) {
	rows, err := s.pool.Query(ctx, `SELECT spec FROM graph.capability_binding ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("graph: list capability-bindings: %w", err)
	}
	defer rows.Close()
	var out []types.CapabilityBinding
	for rows.Next() {
		var spec []byte
		if err := rows.Scan(&spec); err != nil {
			return nil, fmt.Errorf("graph: list capability-bindings: %w", err)
		}
		var b types.CapabilityBinding
		if err := json.Unmarshal(spec, &b); err != nil {
			return nil, fmt.Errorf("graph: decode capability-binding spec: %w", err)
		}
		if types.InScope(b.Environments, s.environment) { // env scope (ADR-0057)
			out = append(out, b)
		}
	}
	return out, rows.Err()
}

// DeleteCapabilityBinding removes one CapabilityBinding declaration.
func (s *Store) DeleteCapabilityBinding(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM graph.capability_binding WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("graph: delete capability-binding: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: capability-binding %s", ErrNotFound, name)
	}
	return nil
}
