package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/dstout-devops/stratt/types"
)

// CreateRun records a new Run summary row (charter §2.3). Only summaries live
// in Postgres; the event stream goes to NATS (§3).
func (s *Store) CreateRun(ctx context.Context, workflowID, viewRef string, viewVersion int64) (types.Run, error) {
	r := types.Run{WorkflowID: workflowID, Status: types.RunPending, ViewRef: viewRef, ViewVersion: viewVersion}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO graph.run (workflow_id, status, view_ref, view_version)
		VALUES ($1, $2, nullif($3, ''), nullif($4, 0))
		RETURNING id, started_at`,
		workflowID, string(r.Status), viewRef, viewVersion,
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

// GetRun returns one Run summary.
func (s *Store) GetRun(ctx context.Context, runID string) (types.Run, error) {
	var r types.Run
	var status string
	var viewRef *string
	var viewVersion *int64
	err := s.pool.QueryRow(ctx, `
		SELECT id, workflow_id, status, view_ref, view_version, started_at, finished_at
		FROM graph.run WHERE id = $1`, runID,
	).Scan(&r.ID, &r.WorkflowID, &status, &viewRef, &viewVersion, &r.StartedAt, &r.FinishedAt)
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
	return r, nil
}
