package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"go.temporal.io/sdk/client"

	"github.com/dstout-devops/stratt/core/internal/contract"
	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

// ErrStartWorkflow marks a failure to start the Temporal workflow (infra, not a
// client error) — callers map it to a 5xx rather than a 4xx.
var ErrStartWorkflow = errors.New("start workflow")

// LaunchDeps are the substrate handles a launch needs — the same ones the API
// Server and the AWX façade already hold.
type LaunchDeps struct {
	Store    *graph.Store
	Temporal client.Client
}

// LaunchParams is the transport-neutral input to launch one Run against a View.
type LaunchParams struct {
	ViewName       string
	Actuator       string // "" defaults to ansible
	Params         json.RawMessage
	CredentialRefs []string
	Slices         int
	Principal      string
}

// LaunchRun is the single launch path shared by POST /api/v1/runs and the AWX
// façade's launch endpoint (§1.6 — one launch, one authz, one audit). It
// validates params against the Actuator Contract at the door (§1.5), pre-creates
// the Run summary, and starts the Temporal workflow. Returns the Run with its
// bound workflow id. A contract violation is returned verbatim (callers map it
// to their own error shape, §1.8).
func LaunchRun(ctx context.Context, d LaunchDeps, p LaunchParams) (types.Run, error) {
	name := p.Actuator
	if name == "" {
		name = "ansible"
	}
	if err := contract.ValidateActuatorParams(name, p.Params); err != nil {
		return types.Run{}, err
	}
	v, err := d.Store.GetView(ctx, p.ViewName)
	if err != nil {
		return types.Run{}, err
	}
	run, err := d.Store.CreateRun(ctx, types.Run{ViewRef: "view://" + v.Name, ViewVersion: v.Version})
	if err != nil {
		return types.Run{}, err
	}
	in := RunInput{
		RunID: run.ID, ViewName: v.Name, Actuator: p.Actuator, Params: p.Params,
		CredentialRefs: p.CredentialRefs, Slices: p.Slices, Principal: p.Principal,
	}
	wfID := "run-" + run.ID
	if _, err := d.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID: wfID, TaskQueue: TaskQueue,
	}, RunAgainstView, in); err != nil {
		_ = d.Store.SetRunStatus(ctx, run.ID, types.RunFailed, map[string]any{"error": "workflow start failed"})
		return types.Run{}, fmt.Errorf("%w: %w", ErrStartWorkflow, err)
	}
	run.WorkflowID = wfID
	if err := d.Store.SetRunWorkflowID(ctx, run.ID, wfID); err != nil {
		return types.Run{}, err
	}
	return run, nil
}

// CancelRun requests cancellation of a Run's Temporal workflow. The Workflow's
// cancellation handler is the single writer of the canceled status and deletes
// the K8s Job(s) — the caller only signals (ADR-0026). Idempotent from the
// caller's view; a missing/complete workflow is not an error worth surfacing.
func CancelRun(ctx context.Context, temporal client.Client, runID string) error {
	if err := temporal.CancelWorkflow(ctx, "run-"+runID, ""); err != nil {
		return fmt.Errorf("cancel run %s: %w", runID, err)
	}
	return nil
}
