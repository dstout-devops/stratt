package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/dstout-devops/stratt/types"
)

// Connectors (charter §2.2, ADR-0103) are CaC-only: every row is a projection of the Git
// declaration, so there is no declared_by column and no API write path — the desired-state
// engine is the sole writer (mirrors graph.trigger).

// UpsertConnector writes one declared Connector.
func (s *Store) UpsertConnector(ctx context.Context, c types.Connector) error {
	spec, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("graph: marshal connector spec: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO graph.connector (name, class, spec)
		VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET class = excluded.class, spec = excluded.spec`,
		c.Name, c.Class, spec)
	if err != nil {
		return fmt.Errorf("graph: upsert connector: %w", err)
	}
	return nil
}

// GetConnector returns one Connector declaration.
func (s *Store) GetConnector(ctx context.Context, name string) (types.Connector, error) {
	var spec []byte
	err := s.pool.QueryRow(ctx,
		`SELECT spec FROM graph.connector WHERE name = $1`, name).Scan(&spec)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.Connector{}, fmt.Errorf("%w: connector %s", ErrNotFound, name)
	}
	if err != nil {
		return types.Connector{}, fmt.Errorf("graph: get connector: %w", err)
	}
	var c types.Connector
	if err := json.Unmarshal(spec, &c); err != nil {
		return c, fmt.Errorf("graph: decode connector spec: %w", err)
	}
	return c, nil
}

// ListConnectors returns every Connector declaration in the active environment, by name.
func (s *Store) ListConnectors(ctx context.Context) ([]types.Connector, error) {
	rows, err := s.pool.Query(ctx, `SELECT spec FROM graph.connector ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("graph: list connectors: %w", err)
	}
	defer rows.Close()
	var out []types.Connector
	for rows.Next() {
		var spec []byte
		if err := rows.Scan(&spec); err != nil {
			return nil, fmt.Errorf("graph: list connectors: %w", err)
		}
		var c types.Connector
		if err := json.Unmarshal(spec, &c); err != nil {
			return nil, fmt.Errorf("graph: decode connector spec: %w", err)
		}
		if types.InScope(c.Environments, s.environment) { // env scope (ADR-0057)
			out = append(out, c)
		}
	}
	return out, rows.Err()
}

// DeleteConnector removes one Connector declaration.
func (s *Store) DeleteConnector(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM graph.connector WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("graph: delete connector: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: connector %s", ErrNotFound, name)
	}
	return nil
}
