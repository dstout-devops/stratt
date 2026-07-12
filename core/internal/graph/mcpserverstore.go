package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/dstout-devops/stratt/types"
)

// MCPServers (ADR-0022) are CaC-only: the desired-state engine is the sole
// writer, mirroring Triggers, Workflows, Emitters, and Baselines.

// UpsertMCPServer writes one declared MCPServer.
func (s *Store) UpsertMCPServer(ctx context.Context, m types.MCPServer) error {
	spec, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("graph: marshal mcp server spec: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO graph.mcp_server (name, spec)
		VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET spec = excluded.spec`,
		m.Name, spec)
	if err != nil {
		return fmt.Errorf("graph: upsert mcp server: %w", err)
	}
	return nil
}

// GetMCPServer returns one MCPServer declaration.
func (s *Store) GetMCPServer(ctx context.Context, name string) (types.MCPServer, error) {
	var spec []byte
	err := s.pool.QueryRow(ctx,
		`SELECT spec FROM graph.mcp_server WHERE name = $1`, name).Scan(&spec)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.MCPServer{}, fmt.Errorf("%w: mcp server %s", ErrNotFound, name)
	}
	if err != nil {
		return types.MCPServer{}, fmt.Errorf("graph: get mcp server: %w", err)
	}
	var m types.MCPServer
	if err := json.Unmarshal(spec, &m); err != nil {
		return m, fmt.Errorf("graph: decode mcp server spec: %w", err)
	}
	return m, nil
}

// ListMCPServers returns every MCPServer declaration, ordered by name.
func (s *Store) ListMCPServers(ctx context.Context) ([]types.MCPServer, error) {
	rows, err := s.pool.Query(ctx, `SELECT spec FROM graph.mcp_server ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("graph: list mcp servers: %w", err)
	}
	defer rows.Close()
	var out []types.MCPServer
	for rows.Next() {
		var spec []byte
		if err := rows.Scan(&spec); err != nil {
			return nil, fmt.Errorf("graph: list mcp servers: %w", err)
		}
		var m types.MCPServer
		if err := json.Unmarshal(spec, &m); err != nil {
			return nil, fmt.Errorf("graph: decode mcp server spec: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteMCPServer removes one MCPServer declaration.
func (s *Store) DeleteMCPServer(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM graph.mcp_server WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("graph: delete mcp server: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: mcp server %s", ErrNotFound, name)
	}
	return nil
}
