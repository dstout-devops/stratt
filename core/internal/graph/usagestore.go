package graph

import (
	"context"
	"fmt"
	"time"

	"github.com/dstout-devops/stratt/types"
)

// ListUsage aggregates MCP tool calls per (principal, tool), optionally
// filtered by principal — the §1.6 accounting made visible. Re-sourced over the
// one audit stream (ADR-0034): MCP calls are now mcp.tool-call audit events, so
// usage and the SIEM forwarder read the same rows.
func (s *Store) ListUsage(ctx context.Context, principal string) ([]types.UsageEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT principal_id, max(principal_kind), object,
		       count(*), count(*) FILTER (WHERE outcome <> 'ok'), max(at)
		FROM audit.event
		WHERE action = 'mcp.tool-call' AND ($1 = '' OR principal_id = $1)
		GROUP BY principal_id, object
		ORDER BY principal_id, object`, principal)
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
