package graph

import (
	"context"
	"fmt"
	"time"

	"github.com/dstout-devops/stratt/types"
)

// RecordMCPCall writes one platform-MCP-server tool invocation — the §1.6
// per-identity usage accounting record (ADR-0021).
func (s *Store) RecordMCPCall(ctx context.Context, c types.MCPCall) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO audit.mcp_call (principal, principal_kind, tool, ok, duration_ms)
		VALUES ($1, $2, $3, $4, $5)`,
		c.Principal, c.PrincipalKind, c.Tool, c.OK, c.DurationMS)
	if err != nil {
		return fmt.Errorf("graph: record mcp call: %w", err)
	}
	return nil
}

// ListUsage aggregates MCP tool calls per (principal, tool), optionally
// filtered by principal — the §1.6 accounting made visible.
func (s *Store) ListUsage(ctx context.Context, principal string) ([]types.UsageEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT principal, max(principal_kind), tool,
		       count(*), count(*) FILTER (WHERE NOT ok), max(at)
		FROM audit.mcp_call
		WHERE ($1 = '' OR principal = $1)
		GROUP BY principal, tool
		ORDER BY principal, tool`, principal)
	if err != nil {
		return nil, fmt.Errorf("graph: list usage: %w", err)
	}
	defer rows.Close()
	var out []types.UsageEntry
	for rows.Next() {
		var u types.UsageEntry
		var last time.Time
		if err := rows.Scan(&u.Principal, &u.PrincipalKind, &u.Tool, &u.Calls, &u.Errors, &last); err != nil {
			return nil, fmt.Errorf("graph: list usage: %w", err)
		}
		u.LastCall = last
		out = append(out, u)
	}
	return out, rows.Err()
}
