package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/dstout-devops/stratt/types"
)

// BaselineObservation is one check Run's verdict on one target: drifted
// (would change) or clean. Failed/unreachable targets never become
// observations — a broken check is evidence of neither (ADR-0019).
type BaselineObservation struct {
	// Drifted is true when the check reported the target would change.
	Drifted bool
	// EntityID resolves the target through the View's membership; empty for
	// non-Entity targets (e.g. an opentofu workspace).
	EntityID string
	// Detail is the redacted, size-capped observed-vs-expected diff.
	Detail json.RawMessage
}

// ObservationOutcome summarizes what one RecordBaselineObservations pass did.
type ObservationOutcome struct {
	Opened   int `json:"opened"`
	Pending  int `json:"pending"`
	Resolved int `json:"resolved"`
	// Cleared counts pending Findings deleted by a clean observation before
	// they ever fired — damping absorbed the flap (§4.3).
	Cleared int `json:"cleared"`
}

// RecordBaselineObservations applies one check Run's observations to the
// Baseline's live Findings in a single transaction — the §4.3 flap-damping
// state machine, with this method as the single writer:
//
//   - drifted: the live row's consecutive counter increments (created as
//     pending); at DampingObservations it opens.
//   - clean: a pending row is deleted (it never fired — no record owed); an
//     open row resolves, kept as the audit history.
//
// Targets absent from obs (failed, unreachable, or no longer in the View)
// cause no transition.
func (s *Store) RecordBaselineObservations(ctx context.Context, b types.Baseline, runID string, obs map[string]BaselineObservation) (ObservationOutcome, error) {
	damping := b.DampingObservations
	if damping < 1 {
		damping = 1
	}
	var out ObservationOutcome

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return out, fmt.Errorf("graph: record observations: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	// Live rows for this Baseline: at most one per target by the partial
	// unique index.
	rows, err := tx.Query(ctx, `
		SELECT id, target, status, consecutive_drifted
		FROM graph.finding
		WHERE baseline = $1 AND status <> 'resolved'
		FOR UPDATE`, b.Name)
	if err != nil {
		return out, fmt.Errorf("graph: record observations: %w", err)
	}
	type liveRow struct {
		id     string
		status string
		count  int
	}
	live := map[string]liveRow{}
	for rows.Next() {
		var r liveRow
		var target string
		if err := rows.Scan(&r.id, &target, &r.status, &r.count); err != nil {
			rows.Close()
			return out, fmt.Errorf("graph: record observations: %w", err)
		}
		live[target] = r
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("graph: record observations: %w", err)
	}

	for target, o := range obs {
		cur, exists := live[target]
		switch {
		case o.Drifted && !exists:
			status := types.FindingPending
			if damping <= 1 {
				status = types.FindingOpen
			}
			_, err = tx.Exec(ctx, `
				INSERT INTO graph.finding
					(baseline, target, entity_id, status, severity, framework,
					 consecutive_drifted, diff, run_id, opened_at)
				VALUES ($1, $2, nullif($3, ''), $4, $5, $6, 1, $7, nullif($8, '')::uuid,
					CASE WHEN $4 = 'open' THEN now() END)`,
				b.Name, target, o.EntityID, status, b.Severity, b.Framework,
				o.Detail, runID)
			if err != nil {
				return out, fmt.Errorf("graph: record observations: insert %s: %w", target, err)
			}
			if status == types.FindingOpen {
				out.Opened++
			} else {
				out.Pending++
			}

		case o.Drifted:
			opens := cur.status == types.FindingPending && cur.count+1 >= damping
			_, err = tx.Exec(ctx, `
				UPDATE graph.finding
				SET consecutive_drifted = consecutive_drifted + 1,
				    diff = $2, run_id = nullif($3, '')::uuid, entity_id = nullif($4, ''),
				    last_observed = now(),
				    status = CASE WHEN $5 THEN 'open' ELSE status END,
				    opened_at = CASE WHEN $5 THEN now() ELSE opened_at END
				WHERE id = $1`,
				cur.id, o.Detail, runID, o.EntityID, opens)
			if err != nil {
				return out, fmt.Errorf("graph: record observations: update %s: %w", target, err)
			}
			if opens {
				out.Opened++
			} else if cur.status == types.FindingPending {
				out.Pending++
			}

		case exists && cur.status == types.FindingPending:
			// Clean before firing: damping absorbed the flap; no record owed.
			if _, err := tx.Exec(ctx, `DELETE FROM graph.finding WHERE id = $1`, cur.id); err != nil {
				return out, fmt.Errorf("graph: record observations: clear %s: %w", target, err)
			}
			out.Cleared++

		case exists: // open → resolved (kept — the audit record)
			_, err = tx.Exec(ctx, `
				UPDATE graph.finding
				SET status = 'resolved', resolved_at = now(),
				    last_observed = now(), consecutive_drifted = 0, run_id = nullif($2, '')::uuid
				WHERE id = $1`, cur.id, runID)
			if err != nil {
				return out, fmt.Errorf("graph: record observations: resolve %s: %w", target, err)
			}
			out.Resolved++
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return out, fmt.Errorf("graph: record observations: %w", err)
	}
	return out, nil
}

const findingColumns = `id, baseline, target, entity_id, status, severity, framework,
	consecutive_drifted, diff, run_id, first_observed, last_observed, opened_at, resolved_at`

func scanFinding(row pgx.Row) (types.Finding, error) {
	var f types.Finding
	var entityID, runID *string
	var diff []byte
	if err := row.Scan(&f.ID, &f.Baseline, &f.Target, &entityID, &f.Status,
		&f.Severity, &f.Framework, &f.ConsecutiveDrifted, &diff, &runID,
		&f.FirstObserved, &f.LastObserved, &f.OpenedAt, &f.ResolvedAt); err != nil {
		return f, err
	}
	if entityID != nil {
		f.EntityID = *entityID
	}
	if runID != nil {
		f.RunID = *runID
	}
	f.Diff = diff
	return f, nil
}

// WriteOrphanFinding records a single open Finding for compiled state left
// behind by a withdrawn-but-retained Assignment (charter §2.4, §4.3:
// abandoned state is never silent). Idempotent on the live (baseline,
// target) row via the partial unique index — a second withdrawal pass just
// refreshes it, never duplicates. runID is empty (the Intent-layer
// withdrawal, not a check Run, is the evidence).
func (s *Store) WriteOrphanFinding(ctx context.Context, baseline, target, severity string, detail []byte) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO graph.finding
			(baseline, target, status, severity, framework, consecutive_drifted, diff, opened_at)
		VALUES ($1, $2, 'open', $3, 'orphan', 1, $4, now())
		ON CONFLICT (baseline, target) WHERE status <> 'resolved'
		DO UPDATE SET diff = excluded.diff, last_observed = now()`,
		baseline, target, severity, detail)
	if err != nil {
		return fmt.Errorf("graph: write orphan finding: %w", err)
	}
	return nil
}

// ListFindings returns Findings, newest observation first, optionally
// filtered by Baseline name and/or status.
func (s *Store) ListFindings(ctx context.Context, baseline, status string, limit int) ([]types.Finding, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+findingColumns+`
		FROM graph.finding
		WHERE ($1 = '' OR baseline = $1) AND ($2 = '' OR status = $2)
		ORDER BY last_observed DESC LIMIT $3`, baseline, status, limit)
	if err != nil {
		return nil, fmt.Errorf("graph: list findings: %w", err)
	}
	defer rows.Close()
	var out []types.Finding
	for rows.Next() {
		f, err := scanFinding(rows)
		if err != nil {
			return nil, fmt.Errorf("graph: list findings: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetFinding returns one Finding.
func (s *Store) GetFinding(ctx context.Context, id string) (types.Finding, error) {
	f, err := scanFinding(s.pool.QueryRow(ctx,
		`SELECT `+findingColumns+` FROM graph.finding WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return f, fmt.Errorf("%w: finding %s", ErrNotFound, id)
	}
	if err != nil {
		return f, fmt.Errorf("graph: get finding: %w", err)
	}
	return f, nil
}
