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

// LaunchParams is the transport-neutral input to launch one Run — against a
// View (Actuator) or as a targetless Action (§2.2, ADR-0031).
type LaunchParams struct {
	ViewName string
	Actuator string // explicit; no platform default (ADR-0046)
	// Action, when set, launches a targetless Connector Action (RunAction)
	// instead of an Actuator Run; ViewName is ignored. DryRun asks for a plan.
	Action         string
	DryRun         bool
	Params         json.RawMessage
	CredentialRefs []string
	Slices         int
	Principal      string
	// FacetWriteScope is the Facet namespaces this Run may write back (ADR-0054).
	FacetWriteScope []string
	// StayLocal launches a Run that must not fan out across Cells (ADR-0044
	// slice 5) — set by the API handler when the request arrived as a verified
	// peer fan-out (a forwarded child Run). A direct launch leaves it false.
	StayLocal bool
}

// LaunchRun is the single launch path shared by POST /api/v1/runs and the AWX
// façade's launch endpoint (§1.6 — one launch, one authz, one audit). It
// validates params against the Actuator Contract at the door (§1.5), pre-creates
// the Run summary, and starts the Temporal workflow. Returns the Run with its
// bound workflow id. A contract violation is returned verbatim (callers map it
// to their own error shape, §1.8).
func LaunchRun(ctx context.Context, d LaunchDeps, p LaunchParams) (types.Run, error) {
	if p.Action != "" {
		return launchAction(ctx, d, p)
	}
	// A View actuation names its Actuator EXPLICITLY — no platform default (ADR-0046:
	// the spine names no tool; every Run's actuator is traceable to a declaration).
	name := p.Actuator
	if name == "" {
		return types.Run{}, fmt.Errorf("a View actuation requires an explicit actuator (no platform default)")
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
		StayLocal: p.StayLocal, FacetWriteScope: p.FacetWriteScope,
	}
	// Cross-Cell selection (ADR-0044 slice 5): a direct launch on a fleet with
	// peer Cells runs the parent RunAcrossCells (scatter a child Run to every
	// Cell, each self-scoping to its home entities). A forwarded child
	// (StayLocal) or a single-Cell estate (no peers) runs RunAgainstView —
	// byte-identically to today. A Cell-registry read error fails the launch
	// loudly rather than silently dropping peer targets (§1.8).
	workflowFn := any(RunAgainstView)
	if !p.StayLocal {
		peers, err := d.Store.PeerCells(ctx)
		if err != nil {
			_ = d.Store.SetRunStatus(ctx, run.ID, types.RunFailed, map[string]any{"error": "cell registry unavailable"})
			return types.Run{}, fmt.Errorf("%w: read peer Cells: %w", ErrStartWorkflow, err)
		}
		if len(peers) > 0 {
			workflowFn = any(RunAcrossCells)
		}
	}
	wfID := "run-" + run.ID
	if _, err := d.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID: wfID, TaskQueue: TaskQueue,
	}, workflowFn, in); err != nil {
		_ = d.Store.SetRunStatus(ctx, run.ID, types.RunFailed, map[string]any{"error": "workflow start failed"})
		return types.Run{}, fmt.Errorf("%w: %w", ErrStartWorkflow, err)
	}
	run.WorkflowID = wfID
	if err := d.Store.SetRunWorkflowID(ctx, run.ID, wfID); err != nil {
		return types.Run{}, err
	}
	return run, nil
}

// launchAction is the single-launch path for a targetless Connector Action
// (§2.2, ADR-0031). It validates params against the Action's INPUT Contract at
// the door (§1.5), pre-creates the Run, and starts RunAction. (Launch-level
// dedup via a stable workflow-id for idempotent Actions is a documented
// follow-up; activity-retry safety already comes from Job-name adoption.)
func launchAction(ctx context.Context, d LaunchDeps, p LaunchParams) (types.Run, error) {
	if err := contract.ValidateActionInput(p.Action, p.Params); err != nil {
		return types.Run{}, err
	}
	run, err := d.Store.CreateRun(ctx, types.Run{ViewRef: "action://" + p.Action})
	if err != nil {
		return types.Run{}, err
	}
	in := RunInput{
		RunID: run.ID, Action: p.Action, DryRun: p.DryRun, Params: p.Params,
		CredentialRefs: p.CredentialRefs, Principal: p.Principal,
	}
	wfID := "run-" + run.ID
	if _, err := d.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID: wfID, TaskQueue: TaskQueue,
	}, RunAction, in); err != nil {
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
