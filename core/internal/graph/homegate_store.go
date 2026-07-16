package graph

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/dstout-devops/stratt/types"
)

// Store support for the ADR-0045 Connector home-ownership supervisor + collision
// reconcile. Runtime standby/active state is NEVER stored here (it lives in the
// daemon's in-memory homegate.Status, §1.2); these are only the DB reads/Findings
// the supervisor and reconcile need.

// LocalHomedSources returns the names of Sources this (named) Cell authoritatively
// homes and are NOT sealed for re-home — the collision reconcile's input set. A
// sealed Source's brief cross-Cell overlap during a fenced re-home is expected and
// excluded. Ordered for determinism.
func (s *Store) LocalHomedSources(ctx context.Context, cell string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT name FROM graph.source WHERE cell = $1 AND rehoming_to IS NULL ORDER BY name`, cell)
	if err != nil {
		return nil, fmt.Errorf("graph: local homed sources: %w", err)
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// WriteSourceCollisionFinding opens/refreshes the CRITICAL Finding that >1 Cell
// homes one Source name (ADR-0045 must-fix 2). One row per Source via the
// (baseline,target) partial unique index.
func (s *Store) WriteSourceCollisionFinding(ctx context.Context, source string, cells []string) error {
	detail, _ := json.Marshal(map[string]any{"cells": cells})
	_, err := s.pool.Exec(ctx, `
		INSERT INTO graph.finding
			(baseline, target, status, severity, framework, consecutive_drifted, diff, opened_at)
		VALUES ($1, $2, 'open', 'critical', $3, 1, $4, now())
		ON CONFLICT (baseline, target) WHERE status <> 'resolved'
		DO UPDATE SET diff = excluded.diff, last_observed = now()`,
		"home-collision:"+source, "source:"+source, types.FindingHomeCollision, detail)
	if err != nil {
		return fmt.Errorf("graph: write source-collision finding: %w", err)
	}
	return nil
}

// ResolveSourceCollisionFinding clears a collision Finding once the Source is
// homed on exactly one Cell again (e.g. a re-home resolved it).
func (s *Store) ResolveSourceCollisionFinding(ctx context.Context, source string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE graph.finding SET status = 'resolved', resolved_at = now(), consecutive_drifted = 0
		WHERE framework = $1 AND target = $2 AND status <> 'resolved'`,
		types.FindingHomeCollision, "source:"+source)
	if err != nil {
		return fmt.Errorf("graph: resolve source-collision finding: %w", err)
	}
	return nil
}

// WriteHomeStandbyFinding opens/refreshes the warning Finding that a Connector is
// standing by because it cannot confirm the Source's fleet home (ADR-0045
// must-fix 4).
func (s *Store) WriteHomeStandbyFinding(ctx context.Context, source, reason string) error {
	detail, _ := json.Marshal(map[string]any{"reason": reason})
	_, err := s.pool.Exec(ctx, `
		INSERT INTO graph.finding
			(baseline, target, status, severity, framework, consecutive_drifted, diff, opened_at)
		VALUES ($1, $2, 'open', 'warning', $3, 1, $4, now())
		ON CONFLICT (baseline, target) WHERE status <> 'resolved'
		DO UPDATE SET diff = excluded.diff, last_observed = now()`,
		"home-standby:"+source, "source:"+source, types.FindingHomeStandby, detail)
	if err != nil {
		return fmt.Errorf("graph: write home-standby finding: %w", err)
	}
	return nil
}

// ResolveHomeStandbyFinding clears a standby Finding once the home resolves.
func (s *Store) ResolveHomeStandbyFinding(ctx context.Context, source string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE graph.finding SET status = 'resolved', resolved_at = now(), consecutive_drifted = 0
		WHERE framework = $1 AND target = $2 AND status <> 'resolved'`,
		types.FindingHomeStandby, "source:"+source)
	if err != nil {
		return fmt.Errorf("graph: resolve home-standby finding: %w", err)
	}
	return nil
}
