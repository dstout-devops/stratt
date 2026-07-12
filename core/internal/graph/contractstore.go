package graph

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/dstout-devops/stratt/types"
)

// ErrContractDrift marks a shipped schema document whose bytes no longer
// match the registered pin for the same name+version — blocking by charter
// (§1.5: "schema drift is blocking"). The fix is a new version, never a
// silent overwrite.
var ErrContractDrift = errors.New("graph: contract drift")

// RegisterContract pins one Contract. Same name+version+hash is a noop;
// same name+version with a different hash is ErrContractDrift carrying both
// hashes; a new name or version inserts.
func (s *Store) RegisterContract(ctx context.Context, c types.Contract) error {
	var existing string
	err := s.pool.QueryRow(ctx,
		`SELECT hash FROM graph.contract WHERE name = $1 AND version = $2`,
		c.Name, c.Version,
	).Scan(&existing)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO graph.contract (name, version, rung, hash, schema)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (name, version) DO NOTHING`,
			c.Name, c.Version, c.Rung, c.Hash, c.Schema); err != nil {
			return fmt.Errorf("graph: register contract: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("graph: register contract: %w", err)
	case existing == c.Hash:
		return nil
	default:
		return fmt.Errorf("%w: %s v%d is pinned to %s but the shipped document hashes to %s — publish a new version, never mutate a pin (§1.5)",
			ErrContractDrift, c.Name, c.Version, existing, c.Hash)
	}
}

// ListContracts returns every pinned Contract, ordered by name+version.
func (s *Store) ListContracts(ctx context.Context) ([]types.Contract, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT name, version, rung, hash, schema
		FROM graph.contract ORDER BY name, version`)
	if err != nil {
		return nil, fmt.Errorf("graph: list contracts: %w", err)
	}
	defer rows.Close()
	var out []types.Contract
	for rows.Next() {
		var c types.Contract
		if err := rows.Scan(&c.Name, &c.Version, &c.Rung, &c.Hash, &c.Schema); err != nil {
			return nil, fmt.Errorf("graph: list contracts: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
