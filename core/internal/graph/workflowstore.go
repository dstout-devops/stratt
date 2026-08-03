package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/dstout-devops/stratt/types"
)

// ErrConflict marks a write refused because the row is already in a
// different terminal state (e.g. deciding a decided Gate).
var ErrConflict = errors.New("graph: conflict")

// Workflows (charter §2, ADR-0011) are CaC-only in v1, like Triggers: the
// desired-state engine is the sole writer of graph.workflow; workflow_run
// and gate rows are written only by the API start path and the RunDAG
// Temporal workflow's activities.

// UpsertWorkflow writes one declared Workflow.
func (s *Store) UpsertWorkflow(ctx context.Context, w types.Workflow) error {
	spec, err := json.Marshal(w)
	if err != nil {
		return fmt.Errorf("graph: marshal workflow spec: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO graph.workflow (name, spec)
		VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET spec = excluded.spec`,
		w.Name, spec)
	if err != nil {
		return fmt.Errorf("graph: upsert workflow: %w", err)
	}
	return nil
}

// GetWorkflow returns one Workflow declaration.
func (s *Store) GetWorkflow(ctx context.Context, name string) (types.Workflow, error) {
	var spec []byte
	err := s.pool.QueryRow(ctx,
		`SELECT spec FROM graph.workflow WHERE name = $1`, name).Scan(&spec)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.Workflow{}, fmt.Errorf("%w: workflow %s", ErrNotFound, name)
	}
	if err != nil {
		return types.Workflow{}, fmt.Errorf("graph: get workflow: %w", err)
	}
	var w types.Workflow
	if err := json.Unmarshal(spec, &w); err != nil {
		return w, fmt.Errorf("graph: decode workflow spec: %w", err)
	}
	return w, nil
}

// ListWorkflows returns every Workflow declaration, ordered by name.
func (s *Store) ListWorkflows(ctx context.Context) ([]types.Workflow, error) {
	rows, err := s.pool.Query(ctx, `SELECT spec FROM graph.workflow ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("graph: list workflows: %w", err)
	}
	defer rows.Close()
	var out []types.Workflow
	for rows.Next() {
		var spec []byte
		if err := rows.Scan(&spec); err != nil {
			return nil, fmt.Errorf("graph: list workflows: %w", err)
		}
		var w types.Workflow
		if err := json.Unmarshal(spec, &w); err != nil {
			return nil, fmt.Errorf("graph: decode workflow spec: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// DeleteWorkflow removes one Workflow declaration.
func (s *Store) DeleteWorkflow(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM graph.workflow WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("graph: delete workflow: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: workflow %s", ErrNotFound, name)
	}
	return nil
}

// ── WorkflowRuns ─────────────────────────────────────────────────────────────

// CreateWorkflowRun records the start of one Workflow execution.
// triggeredBy names the Trigger that fired it ("" = API launch, §1.8).
func (s *Store) CreateWorkflowRun(ctx context.Context, workflowName, temporalID, principal, triggeredBy string) (types.WorkflowRun, error) {
	return s.CreateNestedWorkflowRun(ctx, workflowName, temporalID, principal, triggeredBy, "", "")
}

// CreateNestedWorkflowRun is CreateWorkflowRun with the ADR-0139 D2 parent link. Both parent
// fields are set together or neither is — the data layer enforces it, because a half-written link
// reads as navigable and is not.
func (s *Store) CreateNestedWorkflowRun(ctx context.Context, workflowName, temporalID, principal, triggeredBy, parentRunID, parentStep string) (types.WorkflowRun, error) {
	wr := types.WorkflowRun{
		WorkflowName: workflowName, TemporalID: temporalID,
		Status: types.RunPending, Principal: principal, TriggeredBy: triggeredBy,
		Cell:                s.projCell(), // homes to the creating daemon's Cell (ADR-0044 slice 5)
		ParentWorkflowRunID: parentRunID, ParentStepName: parentStep,
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO graph.workflow_run (workflow_name, temporal_id, principal, triggered_by, cell,
		                                parent_workflow_run_id, parent_step_name)
		VALUES ($1, $2, $3, nullif($4, ''), $5, nullif($6, '')::uuid, nullif($7, ''))
		RETURNING id, started_at`,
		workflowName, temporalID, principal, triggeredBy, wr.Cell, parentRunID, parentStep,
	).Scan(&wr.ID, &wr.StartedAt)
	if err != nil {
		return wr, fmt.Errorf("graph: create workflow run: %w", err)
	}
	return wr, nil
}

// SetWorkflowRunView records the View this execution inherited at launch (ADR-0157 D3). Written
// once, right after the row is created and BEFORE the Temporal execution starts, because it is an
// authorization input for cancel and a row that could be cancelled before it was set would be a row
// whose cancel could not be authorized.
func (s *Store) SetWorkflowRunView(ctx context.Context, id, viewName string) error {
	if viewName == "" {
		return nil // a direct launch inherits nothing; the Steps name their own Views
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE graph.workflow_run SET view_name = $2 WHERE id = $1`, id, viewName)
	if err != nil {
		return fmt.Errorf("graph: set workflow run view: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: workflow run %s", ErrNotFound, id)
	}
	return nil
}

// SetWorkflowRunTemporalID binds the row to its Temporal execution.
func (s *Store) SetWorkflowRunTemporalID(ctx context.Context, id, temporalID string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE graph.workflow_run SET temporal_id = $2 WHERE id = $1`, id, temporalID)
	if err != nil {
		return fmt.Errorf("graph: set workflow run temporal id: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: workflow run %s", ErrNotFound, id)
	}
	return nil
}

// SetWorkflowRunStatus advances the execution; terminal states stamp
// finished_at and merge the per-Step summary.
func (s *Store) SetWorkflowRunStatus(ctx context.Context, id string, status types.RunStatus, summary map[string]any) error {
	doc, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("graph: marshal workflow run summary: %w", err)
	}
	if summary == nil {
		doc = []byte(`{}`)
	}
	terminal := status == types.RunSucceeded || status == types.RunFailed || status == types.RunCanceled || status == types.RunPartial
	tag, err := s.pool.Exec(ctx, `
		UPDATE graph.workflow_run
		SET status = $2,
		    summary = summary || $3::jsonb,
		    finished_at = CASE WHEN $4 THEN now() ELSE finished_at END
		WHERE id = $1`,
		id, string(status), doc, terminal)
	if err != nil {
		return fmt.Errorf("graph: set workflow run status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: workflow run %s", ErrNotFound, id)
	}
	return nil
}

// GetWorkflowRun returns one execution record plus its terminal summary.
func (s *Store) GetWorkflowRun(ctx context.Context, id string) (types.WorkflowRun, map[string]any, error) {
	var wr types.WorkflowRun
	var status string
	var summary []byte
	var triggeredBy *string
	err := s.pool.QueryRow(ctx, `
		SELECT id, workflow_name, temporal_id, status, principal, triggered_by, summary, started_at, finished_at, cell,
		       coalesce(parent_workflow_run_id::text, ''), coalesce(parent_step_name, ''), coalesce(view_name, '')
		FROM graph.workflow_run WHERE id = $1`, id,
	).Scan(&wr.ID, &wr.WorkflowName, &wr.TemporalID, &status, &wr.Principal, &triggeredBy, &summary, &wr.StartedAt, &wr.FinishedAt, &wr.Cell,
		&wr.ParentWorkflowRunID, &wr.ParentStepName, &wr.ViewName)
	if errors.Is(err, pgx.ErrNoRows) {
		return wr, nil, fmt.Errorf("%w: workflow run %s", ErrNotFound, id)
	}
	if err != nil {
		return wr, nil, fmt.Errorf("graph: get workflow run: %w", err)
	}
	wr.Status = types.RunStatus(status)
	if triggeredBy != nil {
		wr.TriggeredBy = *triggeredBy
	}
	var sum map[string]any
	if err := json.Unmarshal(summary, &sum); err != nil {
		return wr, nil, fmt.Errorf("graph: decode workflow run summary: %w", err)
	}
	return wr, sum, nil
}

// GetWorkflowRunByAWXID resolves a WorkflowRun from the synthetic AWX integer id
// the /api/v2 façade exposes for a workflow_job (ADR-0026), via the
// graph.awx_run_id functional index (migration 00048). Int31 collisions resolve
// to the most-recent execution sharing the id — correct for transient polling,
// the same trade GetRunByAWXID makes. Returns ErrNotFound when none hashes to id.
func (s *Store) GetWorkflowRunByAWXID(ctx context.Context, awxID int64) (types.WorkflowRun, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		SELECT id FROM graph.workflow_run
		WHERE graph.awx_run_id(id) = $1
		ORDER BY started_at DESC LIMIT 1`, awxID,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.WorkflowRun{}, fmt.Errorf("%w: workflow run awx-id %d", ErrNotFound, awxID)
	}
	if err != nil {
		return types.WorkflowRun{}, fmt.Errorf("graph: get workflow run by awx id: %w", err)
	}
	wr, _, err := s.GetWorkflowRun(ctx, id)
	return wr, err
}

// ListWorkflowRuns returns recent executions, newest first.
func (s *Store) ListWorkflowRuns(ctx context.Context, limit int) ([]types.WorkflowRun, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, workflow_name, temporal_id, status, principal, triggered_by, started_at, finished_at
		FROM graph.workflow_run ORDER BY started_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("graph: list workflow runs: %w", err)
	}
	defer rows.Close()
	var out []types.WorkflowRun
	for rows.Next() {
		var wr types.WorkflowRun
		var status string
		var triggeredBy *string
		if err := rows.Scan(&wr.ID, &wr.WorkflowName, &wr.TemporalID, &status, &wr.Principal, &triggeredBy, &wr.StartedAt, &wr.FinishedAt); err != nil {
			return nil, fmt.Errorf("graph: list workflow runs: %w", err)
		}
		wr.Status = types.RunStatus(status)
		if triggeredBy != nil {
			wr.TriggeredBy = *triggeredBy
		}
		out = append(out, wr)
	}
	return out, rows.Err()
}

// ── Gates ────────────────────────────────────────────────────────────────────

// CreateGate opens one pending approval for a Gate Step. The approver policy
// is pinned on the row (audit: what authorized the eventual decision).
func (s *Store) CreateGate(ctx context.Context, workflowRunID, step, planDigest string, approvers types.GateApprovers) (types.Gate, error) {
	g := types.Gate{WorkflowRunID: workflowRunID, Step: step, Status: types.GatePending, Approvers: approvers}
	doc, err := json.Marshal(approvers)
	if err != nil {
		return g, fmt.Errorf("graph: marshal gate approvers: %w", err)
	}
	// plan_digest is set ONLY on INSERT and never in the DO UPDATE — WRITE-ONCE
	// (ADR-0047 §8): a retried/re-created Gate keeps the first-bound digest, so a
	// re-plan can never silently rebind a different plan under an approval. The
	// RETURNING reads back whatever is persisted (the first value on a conflict).
	err = s.pool.QueryRow(ctx, `
		INSERT INTO graph.gate (workflow_run_id, step, approvers, plan_digest)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (workflow_run_id, step) DO UPDATE SET step = excluded.step
		RETURNING id, status, created_at, plan_digest`,
		workflowRunID, step, doc, planDigest,
	).Scan(&g.ID, &g.Status, &g.CreatedAt, &g.PlanDigest)
	if err != nil {
		return g, fmt.Errorf("graph: create gate: %w", err)
	}
	return g, nil
}

// DecideGate records the terminal decision; only a pending Gate transitions
// (idempotent across activity retries: re-deciding the same way is a noop).
func (s *Store) DecideGate(ctx context.Context, id, status, decidedBy, note string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE graph.gate
		SET status = $2, decided_by = $3, note = $4, decided_at = now()
		WHERE id = $1 AND (status = 'pending' OR (status = $2 AND decided_by = $3))`,
		id, status, decidedBy, note)
	if err != nil {
		return fmt.Errorf("graph: decide gate: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: gate %s is not pending", ErrConflict, id)
	}
	return nil
}

// GetGate returns one Gate.
func (s *Store) GetGate(ctx context.Context, id string) (types.Gate, error) {
	return s.scanGate(s.pool.QueryRow(ctx, gateSelect+` WHERE id = $1`, id))
}

// ListGates returns Gates filtered by status ("" = all), newest first.
func (s *Store) ListGates(ctx context.Context, status string) ([]types.Gate, error) {
	rows, err := s.pool.Query(ctx,
		gateSelect+` WHERE ($1 = '' OR status = $1) ORDER BY created_at DESC`, status)
	if err != nil {
		return nil, fmt.Errorf("graph: list gates: %w", err)
	}
	defer rows.Close()
	var out []types.Gate
	for rows.Next() {
		g, err := s.scanGate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ListGatesForWorkflowRun returns the Gates of one execution.
func (s *Store) ListGatesForWorkflowRun(ctx context.Context, workflowRunID string) ([]types.Gate, error) {
	rows, err := s.pool.Query(ctx,
		gateSelect+` WHERE workflow_run_id = $1 ORDER BY created_at`, workflowRunID)
	if err != nil {
		return nil, fmt.Errorf("graph: list gates for workflow run: %w", err)
	}
	defer rows.Close()
	var out []types.Gate
	for rows.Next() {
		g, err := s.scanGate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

const gateSelect = `
	SELECT id, workflow_run_id, step, status, approvers, decided_by, note, created_at, decided_at, plan_digest
	FROM graph.gate`

func (s *Store) scanGate(row pgx.Row) (types.Gate, error) {
	var g types.Gate
	var approvers []byte
	err := row.Scan(&g.ID, &g.WorkflowRunID, &g.Step, &g.Status, &approvers,
		&g.DecidedBy, &g.Note, &g.CreatedAt, &g.DecidedAt, &g.PlanDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, fmt.Errorf("%w: gate", ErrNotFound)
	}
	if err != nil {
		return g, fmt.Errorf("graph: scan gate: %w", err)
	}
	if err := json.Unmarshal(approvers, &g.Approvers); err != nil {
		return g, fmt.Errorf("graph: decode gate approvers: %w", err)
	}
	return g, nil
}
