package graph

import (
	"context"
	"fmt"

	"github.com/dstout-devops/stratt/types"
)

// DecommissionCandidate is a BUILT Entity that carries a stratt.intent/instance correlation label — a
// unit the decommission reach-path (ADR-0114 D4) may tear down. Name is the correlation label value
// (e.g. "web-05"); Kind is the Entity kind.
//
// IdentityKeys is scheme → value for every identity the Entity carries, and the reconcile needs
// EXACTLY ONE of them to name a teardown target. Plural here rather than a single chosen identity
// because choosing is a decision with a fail-closed branch (see soleIdentity): core will not pick
// between two identities for a destructive act (§2.4), so the store reports what is there and the
// reconcile decides.
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

// DecommissionFinding is one gated teardown to record, with the launch spec that makes it
// launchable through the ordinary remediation door (ADR-0120 D1).
type DecommissionFinding struct {
	Baseline string
	Target   string
	Severity string
	Detail   []byte
	// LaunchWorkflow is the resolved provider's teardown Workflow, empty when resolution
	// did not land — in which case NO launch spec is written at all (fail-closed, §2.4).
	LaunchWorkflow string
	LaunchParams   map[string]any
}

// WriteDecommissionFinding records/refreshes one open decommission Finding (ADR-0114 D4): a GATED
// teardown the operator must launch (§5 Flow, destructive ⇒ gated). Keyed to the Intent via baseline
// "decommission/<intent>"; target is the excess instance name. framework = 'decommission' keeps it a
// DISTINCT population from build Findings ('provision'), so the two reconciles never resolve each other.
//
// The launch spec is written in TYPED COLUMNS, exactly as the provision Finding's is, and this is a
// correction rather than an addition. The teardown Workflow and the target's identity used to live only
// inside the `diff` detail blob, so `POST /findings/{id}/remediation` fell through to a Baseline read —
// and there IS no `decommission/<intent>` Baseline, nothing creates one — and answered 404 "nothing to
// launch". The reach-path ADR-0114 D4 built reached a door that refused it, leaving the operator to read
// the blob and hand-launch the Workflow by name with an identity they retyped. That also sidesteps D4's
// build-provenance anchor, which is the whole point of resolving the teardown of the provider that built
// the thing. `diff` is documented as redacted and size-capped, so parsing a launch back out of it was
// never an option (ADR-0118 made the same call for the withdrawal path).
//
// launch_kind is `remove`, NOT a fourth enum member — see the argument in ADR-0114's record: a count-down
// teardown retires state whose declaration no longer asks for it, which is what `remove` already names.
//
// Like the provision Finding, the spec is REDERIVED and refreshed on conflict: a rebound provider or a
// re-observed identity must reach an already-open Finding, or the operator launches a stale target.
func (s *Store) WriteDecommissionFinding(ctx context.Context, f DecommissionFinding) error {
	params, err := marshalLaunchParams(f.LaunchParams)
	if err != nil {
		return fmt.Errorf("graph: decommission finding: %w", err)
	}
	kind := ""
	if f.LaunchWorkflow != "" {
		kind = types.LaunchRemove
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO graph.finding
			(baseline, target, status, severity, framework, consecutive_drifted, diff, opened_at,
			 launch_workflow, launch_params, launch_kind)
		VALUES ($1, $2, 'open', $3, 'decommission', 1, $4, now(), nullif($5, ''), $6, nullif($7, ''))
		ON CONFLICT (baseline, target) WHERE status <> 'resolved'
		DO UPDATE SET diff = excluded.diff, last_observed = now(),
			launch_workflow = excluded.launch_workflow, launch_params = excluded.launch_params,
			launch_kind = excluded.launch_kind`,
		f.Baseline, f.Target, f.Severity, f.Detail, f.LaunchWorkflow, params, kind)
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
