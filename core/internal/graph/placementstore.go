package graph

import (
	"context"
	"fmt"
)

// ObservedPlacements returns, per built Entity carrying correlationLabel, the NAMES of
// the subnets it is observed placed-in — its placed-in edges resolved to the target
// subnet's net.subnet.name (the subnet's canonical name every Source stamps: a portgroup
// name, a Claim name, an Intent/Subnet name). This is the OBSERVED side of the
// placement-drift check (ADR-0059 decision 5, S5): the declared placement.subnet is
// compared against these. Only live projections are consulted (§1.2) — a decommissioned
// host or a retracted edge simply drops out (the relation GC keeps it honest).
func (s *Store) ObservedPlacements(ctx context.Context, correlationLabel string) (map[string][]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT unit.labels->>$1 AS corr, sub_facet.value->>'name' AS subnet_name
		FROM graph.entity unit
		JOIN graph.relation r      ON r.from_id = unit.id AND r.type = 'placed-in'
		JOIN graph.entity sub      ON sub.id = r.to_id AND sub.kind = 'subnet' AND sub.deleted_at IS NULL
		JOIN graph.facet sub_facet ON sub_facet.entity_id = sub.id AND sub_facet.namespace = 'net.subnet'
		WHERE unit.labels ? $1 AND unit.deleted_at IS NULL
		  AND sub_facet.value->>'name' IS NOT NULL`, correlationLabel)
	if err != nil {
		return nil, fmt.Errorf("graph: observed placements: %w", err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var corr, subnet string
		if err := rows.Scan(&corr, &subnet); err != nil {
			return nil, fmt.Errorf("graph: scan observed placement: %w", err)
		}
		if corr != "" && subnet != "" {
			out[corr] = append(out[corr], subnet)
		}
	}
	return out, rows.Err()
}

// WritePlacementDriftFinding records/refreshes one open placement-drift Finding
// (ADR-0059 S5, §1.8): a unit's declared placement diverges from its observed placement.
// framework 'placement'; keyed to the unit via baseline "placement/<unit>". The unit is
// a correlation label value, NOT an Entity id, so entity_id stays null (no phantom, §1.2).
// Idempotent on the live (baseline,target) row; recomputed every reconcile.
func (s *Store) WritePlacementDriftFinding(ctx context.Context, unit string, detail []byte) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO graph.finding
			(baseline, target, status, severity, framework, consecutive_drifted, diff, opened_at)
		VALUES ($1, $2, 'open', 'warning', 'placement', 1, $3, now())
		ON CONFLICT (baseline, target) WHERE status <> 'resolved'
		DO UPDATE SET diff = excluded.diff, last_observed = now()`,
		"placement/"+unit, unit, detail)
	if err != nil {
		return fmt.Errorf("graph: write placement drift finding: %w", err)
	}
	return nil
}

// ResolvePlacementDriftFindingsExcept resolves every open placement-drift Finding whose
// target is NOT in the kept set — units that have since converged (declared == observed),
// stopped being observed, or whose Intent/placement was withdrawn. keep is the set of
// units the current reconcile just re-asserted as drifting. Empty keep resolves them all.
// The convergence half — a Finding never lingers past its drift (§1.8, ADR-0043 GC).
func (s *Store) ResolvePlacementDriftFindingsExcept(ctx context.Context, keep []string) (int64, error) {
	if keep == nil {
		keep = []string{} // a nil slice becomes SQL NULL; ANY(NULL) is NULL, not FALSE
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE graph.finding f
		SET status = 'resolved', resolved_at = now(), last_observed = now(),
		    consecutive_drifted = 0, resolved_reason = 'placement-converged'
		WHERE f.framework = 'placement' AND f.status <> 'resolved'
		  AND NOT (f.target = ANY($1::text[]))`,
		keep)
	if err != nil {
		return 0, fmt.Errorf("graph: resolve placement drift findings: %w", err)
	}
	return tag.RowsAffected(), nil
}
