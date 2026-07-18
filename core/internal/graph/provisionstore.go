package graph

import (
	"context"
	"fmt"
)

// ProvisionedInstances returns the set of stratt.intent/instance correlation
// labels currently projected on live Entities (ADR-0058): the "built" set the
// provisioning reconcile compares an Intent/Compute's desired count against. Only
// projections are consulted — the graph never holds the unbuilt (§1.2), so a
// missing instance is simply absent here, never a phantom row.
func (s *Store) ProvisionedInstances(ctx context.Context) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT labels->>'stratt.intent/instance'
		FROM graph.entity
		WHERE labels ? 'stratt.intent/instance' AND deleted_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("graph: provisioned instances: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("graph: scan provisioned instance: %w", err)
		}
		if name != "" {
			out[name] = true
		}
	}
	return out, rows.Err()
}

// ProvisionedSingletons returns the set of stratt.intent/singleton correlation
// keys (kind/name) currently projected on live Entities (ADR-0059 decision 4): the
// "built" set the named-singleton provisioning reconcile compares desired subnets/
// dns-records/dmzs against. Per-kind namespaced so a singleton never collides with a
// Compute instance. Only projections are consulted — the graph never holds the
// unbuilt (§1.2).
func (s *Store) ProvisionedSingletons(ctx context.Context) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT labels->>'stratt.intent/singleton'
		FROM graph.entity
		WHERE labels ? 'stratt.intent/singleton' AND deleted_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("graph: provisioned singletons: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("graph: scan provisioned singleton: %w", err)
		}
		if key != "" {
			out[key] = true
		}
	}
	return out, rows.Err()
}

// WriteProvisionFinding records/refreshes one open provisioning Finding
// (ADR-0058): a gated build the operator must launch (§5 Flow 1). It is keyed to
// the INTENT via baseline "provision/<intent>"; target is the desired instance
// name, which is NOT an Entity — entity_id stays null, so there is no phantom for
// the unbuilt (§1.2 / guardian M2). Idempotent on the live (baseline,target) row;
// recomputed every reconcile.
func (s *Store) WriteProvisionFinding(ctx context.Context, baseline, target, severity string, detail []byte) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO graph.finding
			(baseline, target, status, severity, framework, consecutive_drifted, diff, opened_at)
		VALUES ($1, $2, 'open', $3, 'provision', 1, $4, now())
		ON CONFLICT (baseline, target) WHERE status <> 'resolved'
		DO UPDATE SET diff = excluded.diff, last_observed = now()`,
		baseline, target, severity, detail)
	if err != nil {
		return fmt.Errorf("graph: write provision finding: %w", err)
	}
	return nil
}

// ResolveProvisionFindingsExcept resolves every open provisioning Finding whose
// (baseline,target) is NOT in the kept set — i.e. instances that have since been
// BUILT (they now project, dropping out of the shortfall) or whose Intent was
// withdrawn. keepBaselines[i]/keepTargets[i] are parallel arrays of the pairs the
// current reconcile just re-asserted. Empty keep resolves them all (every Intent
// withdrawn). This is the convergence half — a Finding never lingers past its
// build (§1.8, ADR-0043 GC discipline).
func (s *Store) ResolveProvisionFindingsExcept(ctx context.Context, keepBaselines, keepTargets []string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE graph.finding f
		SET status = 'resolved', resolved_at = now(), last_observed = now(),
		    consecutive_drifted = 0, resolved_reason = 'provisioned'
		WHERE f.framework = 'provision' AND f.status <> 'resolved'
		  AND NOT EXISTS (
		      SELECT 1 FROM unnest($1::text[], $2::text[]) AS k(baseline, target)
		      WHERE k.baseline = f.baseline AND k.target = f.target)`,
		keepBaselines, keepTargets)
	if err != nil {
		return 0, fmt.Errorf("graph: resolve provision findings: %w", err)
	}
	return tag.RowsAffected(), nil
}
