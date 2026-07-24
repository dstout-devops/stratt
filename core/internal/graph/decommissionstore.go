package graph

import (
	"context"
	"fmt"
)

// DecommissionCandidate is a BUILT Entity that carries a stratt.intent/instance correlation label — a
// unit the decommission reach-path (ADR-0114 D4) may tear down. Name is the correlation label value
// (e.g. "web-05"); IdentityKeys are the Entity's identity schemes (the teardown Action targets the
// provider-specific one, e.g. vcenter.uuid). Kind is the Entity kind.
type DecommissionCandidate struct {
	Name         string
	Kind         string
	IdentityKeys map[string]string
}

// DecommissionCandidates returns every live, correlated built Entity with its identity keys (ADR-0114
// D4). Only projections are consulted (§1.2) — a torn-down unit simply drops out on the next sync. The
// reconcile computes count-down excess from these and pairs each excess name with its identity to build
// the gated teardown Finding.
func (s *Store) DecommissionCandidates(ctx context.Context) ([]DecommissionCandidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT e.id, e.labels->>'stratt.intent/instance', e.kind, i.scheme, i.value
		FROM graph.entity e
		JOIN graph.entity_identity i ON i.entity_id = e.id
		WHERE e.labels ? 'stratt.intent/instance' AND e.deleted_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("graph: decommission candidates: %w", err)
	}
	defer rows.Close()
	byID := map[string]*DecommissionCandidate{}
	var order []string
	for rows.Next() {
		var id, name, kind, scheme, value string
		if err := rows.Scan(&id, &name, &kind, &scheme, &value); err != nil {
			return nil, fmt.Errorf("graph: scan decommission candidate: %w", err)
		}
		c, ok := byID[id]
		if !ok {
			c = &DecommissionCandidate{Name: name, Kind: kind, IdentityKeys: map[string]string{}}
			byID[id] = c
			order = append(order, id)
		}
		if scheme != "" {
			c.IdentityKeys[scheme] = value
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]DecommissionCandidate, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}

// WriteDecommissionFinding records/refreshes one open decommission Finding (ADR-0114 D4): a GATED
// teardown the operator must launch (§5 Flow, destructive ⇒ gated). Keyed to the Intent via baseline
// "decommission/<intent>"; target is the excess instance name. framework = 'decommission' keeps it a
// DISTINCT population from build Findings ('provision'), so the two reconciles never resolve each other.
func (s *Store) WriteDecommissionFinding(ctx context.Context, baseline, target, severity string, detail []byte) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO graph.finding
			(baseline, target, status, severity, framework, consecutive_drifted, diff, opened_at)
		VALUES ($1, $2, 'open', $3, 'decommission', 1, $4, now())
		ON CONFLICT (baseline, target) WHERE status <> 'resolved'
		DO UPDATE SET diff = excluded.diff, last_observed = now()`,
		baseline, target, severity, detail)
	if err != nil {
		return fmt.Errorf("graph: write decommission finding: %w", err)
	}
	return nil
}

// ResolveDecommissionFindingsExcept resolves every open decommission Finding whose (baseline,target) is
// NOT in the kept set — i.e. units that have since been TORN DOWN (they no longer project, so they drop
// out of the excess set) or whose Intent's count was raised back. The convergence half (§1.8): a
// decommission Finding never lingers past its teardown.
func (s *Store) ResolveDecommissionFindingsExcept(ctx context.Context, keepBaselines, keepTargets []string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE graph.finding f
		SET status = 'resolved', resolved_at = now(), last_observed = now(),
		    consecutive_drifted = 0, resolved_reason = 'decommissioned'
		WHERE f.framework = 'decommission' AND f.status <> 'resolved'
		  AND NOT EXISTS (
		      SELECT 1 FROM unnest($1::text[], $2::text[]) AS k(baseline, target)
		      WHERE k.baseline = f.baseline AND k.target = f.target)`,
		keepBaselines, keepTargets)
	if err != nil {
		return 0, fmt.Errorf("graph: resolve decommission findings: %w", err)
	}
	return tag.RowsAffected(), nil
}
