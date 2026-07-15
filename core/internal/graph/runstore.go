package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/dstout-devops/stratt/types"
)

// CreateRun records a new Run summary row seeded from r (charter §2.3): the
// caller sets the descent linkage that applies — TriggeredBy for Trigger
// fires, WorkflowRunID/StepName for Workflow Steps, neither for direct API
// launches (§1.8). Only summaries live in Postgres; events go to NATS (§3).
func (s *Store) CreateRun(ctx context.Context, r types.Run) (types.Run, error) {
	r.Status = types.RunPending
	err := s.pool.QueryRow(ctx, `
		INSERT INTO graph.run (workflow_id, status, view_ref, view_version, triggered_by, baseline, workflow_run_id, step_name)
		VALUES ($1, $2, nullif($3, ''), nullif($4, 0), nullif($5, ''), nullif($6, ''), nullif($7, '')::uuid, nullif($8, ''))
		RETURNING id, started_at`,
		r.WorkflowID, string(r.Status), r.ViewRef, r.ViewVersion, r.TriggeredBy, r.Baseline, r.WorkflowRunID, r.StepName,
	).Scan(&r.ID, &r.StartedAt)
	if err != nil {
		return r, fmt.Errorf("graph: create run: %w", err)
	}
	return r, nil
}

// SetRunWorkflowID binds the Run summary to its Temporal workflow execution.
func (s *Store) SetRunWorkflowID(ctx context.Context, runID, workflowID string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE graph.run SET workflow_id = $2 WHERE id = $1`, runID, workflowID)
	if err != nil {
		return fmt.Errorf("graph: set run workflow id: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: run %s", ErrNotFound, runID)
	}
	return nil
}

// SetRunStatus advances a Run's lifecycle; terminal states stamp finished_at.
func (s *Store) SetRunStatus(ctx context.Context, runID string, status types.RunStatus, summary map[string]any) error {
	doc, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("graph: marshal run summary: %w", err)
	}
	if summary == nil {
		doc = []byte(`{}`)
	}
	terminal := status == types.RunSucceeded || status == types.RunFailed || status == types.RunCanceled
	tag, err := s.pool.Exec(ctx, `
		UPDATE graph.run
		SET status = $2,
		    summary = summary || $3::jsonb,
		    finished_at = CASE WHEN $4 THEN now() ELSE finished_at END
		WHERE id = $1`,
		runID, string(status), doc, terminal)
	if err != nil {
		return fmt.Errorf("graph: set run status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: run %s", ErrNotFound, runID)
	}
	return nil
}

// SetRunOutputs stores an Action Run's typed output values (§2.2, ADR-0031),
// already validated against the Action's output Contract by the caller.
func (s *Store) SetRunOutputs(ctx context.Context, runID string, outputs json.RawMessage) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE graph.run SET outputs = $2::jsonb WHERE id = $1`, runID, string(outputs))
	if err != nil {
		return fmt.Errorf("graph: set run outputs: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: run %s", ErrNotFound, runID)
	}
	return nil
}

// SetRunSites records the union of execution loci a Run touched (ADR-0032) —
// the §1.8 "where did this run" descent answer. Nil/empty leaves it null.
func (s *Store) SetRunSites(ctx context.Context, runID string, sites []string) error {
	if len(sites) == 0 {
		return nil
	}
	blob, err := json.Marshal(sites)
	if err != nil {
		return fmt.Errorf("graph: marshal run sites: %w", err)
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE graph.run SET sites = $2::jsonb WHERE id = $1`, runID, string(blob))
	if err != nil {
		return fmt.Errorf("graph: set run sites: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: run %s", ErrNotFound, runID)
	}
	return nil
}

// ListRunsForWorkflowRun returns the per-Step Runs of one Workflow
// execution — the §1.8 Workflow → Run descent query.
func (s *Store) ListRunsForWorkflowRun(ctx context.Context, workflowRunID string) ([]types.Run, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, step_name, workflow_id, status, started_at, finished_at
		FROM graph.run WHERE workflow_run_id = $1 ORDER BY started_at`, workflowRunID)
	if err != nil {
		return nil, fmt.Errorf("graph: list runs for workflow run: %w", err)
	}
	defer rows.Close()
	var out []types.Run
	for rows.Next() {
		var r types.Run
		var status string
		var stepName *string
		if err := rows.Scan(&r.ID, &stepName, &r.WorkflowID, &status, &r.StartedAt, &r.FinishedAt); err != nil {
			return nil, fmt.Errorf("graph: list runs for workflow run: %w", err)
		}
		r.Status = types.RunStatus(status)
		r.WorkflowRunID = workflowRunID
		if stepName != nil {
			r.StepName = *stepName
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListRuns returns the most recent Run summaries, newest first.
func (s *Store) ListRuns(ctx context.Context, limit int) ([]types.Run, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, workflow_id, status, view_ref, view_version, triggered_by, baseline, workflow_run_id, step_name, started_at, finished_at
		FROM graph.run ORDER BY started_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("graph: list runs: %w", err)
	}
	defer rows.Close()
	var out []types.Run
	for rows.Next() {
		var r types.Run
		var status string
		var viewRef, triggeredBy, baseline, workflowRunID, stepName *string
		var viewVersion *int64
		if err := rows.Scan(&r.ID, &r.WorkflowID, &status, &viewRef, &viewVersion,
			&triggeredBy, &baseline, &workflowRunID, &stepName, &r.StartedAt, &r.FinishedAt); err != nil {
			return nil, fmt.Errorf("graph: list runs: %w", err)
		}
		r.Status = types.RunStatus(status)
		if viewRef != nil {
			r.ViewRef = *viewRef
		}
		if viewVersion != nil {
			r.ViewVersion = *viewVersion
		}
		if triggeredBy != nil {
			r.TriggeredBy = *triggeredBy
		}
		if baseline != nil {
			r.Baseline = *baseline
		}
		if workflowRunID != nil {
			r.WorkflowRunID = *workflowRunID
		}
		if stepName != nil {
			r.StepName = *stepName
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRun returns one Run summary.
func (s *Store) GetRun(ctx context.Context, runID string) (types.Run, error) {
	var r types.Run
	var status string
	var viewRef *string
	var viewVersion *int64
	var triggeredBy, baseline, workflowRunID, stepName *string
	var outputs, sites []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, workflow_id, status, view_ref, view_version, triggered_by, baseline, workflow_run_id, step_name, started_at, finished_at, outputs, sites
		FROM graph.run WHERE id = $1`, runID,
	).Scan(&r.ID, &r.WorkflowID, &status, &viewRef, &viewVersion, &triggeredBy, &baseline, &workflowRunID, &stepName, &r.StartedAt, &r.FinishedAt, &outputs, &sites)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, fmt.Errorf("%w: run %s", ErrNotFound, runID)
	}
	if err != nil {
		return r, fmt.Errorf("graph: get run: %w", err)
	}
	r.Status = types.RunStatus(status)
	if viewRef != nil {
		r.ViewRef = *viewRef
	}
	if viewVersion != nil {
		r.ViewVersion = *viewVersion
	}
	if triggeredBy != nil {
		r.TriggeredBy = *triggeredBy
	}
	if baseline != nil {
		r.Baseline = *baseline
	}
	if workflowRunID != nil {
		r.WorkflowRunID = *workflowRunID
	}
	if stepName != nil {
		r.StepName = *stepName
	}
	if len(outputs) > 0 {
		r.Outputs = outputs
	}
	if len(sites) > 0 {
		if err := json.Unmarshal(sites, &r.Sites); err != nil {
			return r, fmt.Errorf("graph: decode run sites: %w", err)
		}
	}
	return r, nil
}

// GetRunByAWXID resolves a Run from the synthetic AWX integer job id the
// /api/v2 façade exposes (ADR-0026), via the graph.awx_run_id functional index.
// Int31 collisions resolve to the most-recent Run sharing the id — correct for
// transient job polling. Returns ErrNotFound when no Run hashes to id.
// GetRunByWorkflowID returns the Run (id + status) for a Temporal workflow id.
// Used to resolve a delivery Run from the notifier's deterministic workflow id
// even when the workflow failed terminally (the outcome is empty then), so the
// notify_delivery → Run cross-link is never dropped (§1.8, ADR-0040).
func (s *Store) GetRunByWorkflowID(ctx context.Context, workflowID string) (types.Run, error) {
	var r types.Run
	var status string
	err := s.pool.QueryRow(ctx,
		`SELECT id, status FROM graph.run WHERE workflow_id = $1 ORDER BY started_at DESC NULLS LAST LIMIT 1`, workflowID,
	).Scan(&r.ID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, fmt.Errorf("%w: run for workflow %s", ErrNotFound, workflowID)
	}
	if err != nil {
		return r, fmt.Errorf("graph: get run by workflow id: %w", err)
	}
	r.WorkflowID = workflowID
	r.Status = types.RunStatus(status)
	return r, nil
}

func (s *Store) GetRunByAWXID(ctx context.Context, awxID int64) (types.Run, error) {
	var runID string
	err := s.pool.QueryRow(ctx, `
		SELECT id FROM graph.run
		WHERE graph.awx_run_id(id) = $1
		ORDER BY started_at DESC LIMIT 1`, awxID,
	).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.Run{}, fmt.Errorf("%w: run awx-id %d", ErrNotFound, awxID)
	}
	if err != nil {
		return types.Run{}, fmt.Errorf("graph: get run by awx id: %w", err)
	}
	return s.GetRun(ctx, runID)
}
