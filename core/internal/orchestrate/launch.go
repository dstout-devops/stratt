package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"go.temporal.io/sdk/client"

	"github.com/dstout-devops/stratt/core/internal/contract"
	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/policy"
	"github.com/dstout-devops/stratt/types"
)

// ErrStartWorkflow marks a failure to start the Temporal workflow (infra, not a
// client error) — callers map it to a 5xx rather than a 4xx.
var ErrStartWorkflow = errors.New("start workflow")

// ErrLaunchInput marks a launch refused by what the CALLER supplied — declared
// launch inputs that violate the Workflow's interface, or a change context that
// is not admissible. Callers map it to a 4xx; everything else is theirs or ours.
var ErrLaunchInput = errors.New("invalid launch")

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
	// An input Contract belongs to the tool, not to the local name an estate gives one
	// of its Actuators, so resolve the declaration's pluginIdentity first (ADR-0117
	// D3a). Not-found is the normal case for a boot-registered Actuator — its name IS
	// its identity — and leaves resolution exactly where it was.
	if err := contract.ValidateActuatorParamsFor(name, d.Store.PluginIdentityOf(ctx, name), p.Params); err != nil {
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

// WorkflowLaunchParams is the transport-neutral input to launch a WORKFLOW —
// the whole declared DAG, as opposed to LaunchParams' single Run.
//
// It carries no Environment field on purpose: the floor's own environment is
// read from the Store here, never accepted from the caller (ADR-0122 D2).
type WorkflowLaunchParams struct {
	Workflow  types.Workflow
	Principal string
	// Inputs are the caller's answers to the Workflow's declared `inputs`
	// interface; Context is what the launcher asserts about the CHANGE for
	// policy Steps to decide on. Two concepts, two fields (ADR-0118 D4).
	Inputs  map[string]any
	Context map[string]any
	// EntityScope narrows the launched DAG to one Entity (ADR-0150 D3); "" is
	// the whole View. ViewName is the View an actuation Step with none of its
	// own inherits (a Finding remediation); "" for a direct launch.
	EntityScope string
	ViewName    string
}

// LaunchWorkflowRun is THE launch path for a declared Workflow, shared by every
// door that has one: POST /api/v1/workflows/{name}/runs, the remediation door,
// and the AWX façade's workflow_job_templates launch (§1.6 — one launch, one
// validation, one audit).
//
// One function on purpose, and the reason is written into the history: a second
// launch path grows its own authz check, its own validation, and its own drift,
// which is the §1.6 asymmetry that let MCP POST a nil body for as long as it
// did. When the AWX façade gained this family it did NOT get a private copy of
// the sequence — it calls this, exactly as its job_template launch calls
// LaunchRun.
//
// Both validations run HERE rather than inside the DAG so a caller gets an error
// naming the offending input, instead of a created-then-failed WorkflowRun they
// have to go read (§1.8). The RunDAG chokepoint calls the same functions again,
// because that is the one nothing can skip.
//
// AUTHORIZATION IS THE CALLER'S. This function does not check View-runner grants:
// the doors do, each against its own transport's Principal (api.authorizeLaunch,
// the façade's requireRunner). That split is deliberate — the authz decision needs
// the request context — but it means a new door MUST authorize before calling.
func LaunchWorkflowRun(ctx context.Context, d LaunchDeps, p WorkflowLaunchParams) (types.WorkflowRun, error) {
	resolved, err := contract.ResolveLaunchInputs(p.Workflow.Name, p.Workflow.Inputs, p.Inputs)
	if err != nil {
		return types.WorkflowRun{}, fmt.Errorf("%w: %w", ErrLaunchInput, err)
	}
	if err := policy.ValidateChangeContext(p.Context); err != nil {
		return types.WorkflowRun{}, fmt.Errorf("%w: %w", ErrLaunchInput, err)
	}
	wr, err := d.Store.CreateWorkflowRun(ctx, p.Workflow.Name, "", p.Principal, "")
	if err != nil {
		return types.WorkflowRun{}, err
	}
	// The inherited View, recorded BEFORE the execution starts (ADR-0157 D3): it is the
	// authorization input for a later cancel, and a row that could be cancelled before it was set
	// would be a row whose cancel could not be authorized. No-op for a direct launch.
	if err := d.Store.SetWorkflowRunView(ctx, wr.ID, p.ViewName); err != nil {
		return types.WorkflowRun{}, err
	}
	temporalID := "wfrun-" + wr.ID
	if _, err := d.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID: temporalID, TaskQueue: TaskQueue,
	}, RunDAG, DAGInput{
		WorkflowRunID: wr.ID, WorkflowName: p.Workflow.Name, Principal: p.Principal,
		LaunchParams: resolved, Context: p.Context,
		EntityScope: p.EntityScope, ViewName: p.ViewName,
		// The floor's own environment, not the caller's claim about it (ADR-0122 D2).
		Environment: d.Store.ActiveEnvironment(),
	}); err != nil {
		_ = d.Store.SetWorkflowRunStatus(ctx, wr.ID, types.RunFailed, map[string]any{"error": "workflow start failed"})
		return types.WorkflowRun{}, fmt.Errorf("%w: start workflow run: %w", ErrStartWorkflow, err)
	}
	wr.TemporalID = temporalID
	if err := d.Store.SetWorkflowRunTemporalID(ctx, wr.ID, temporalID); err != nil {
		return types.WorkflowRun{}, err
	}
	return wr, nil
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

// CancelWorkflowRun requests cancellation of a WorkflowRun's DAG execution (ADR-0157). Same
// division of labour as CancelRun: the caller only SIGNALS, and RunDAG's own handler is the single
// writer of the terminal status (§2.1) — it stamps `canceled`, records pending Gates, and writes
// the per-Step summary. Children need nothing here: ParentClosePolicy REQUEST_CANCEL propagates to
// every one of them, and each already reaps its own K8s Job and stamps its own row.
//
// The stored TemporalID is used rather than a reconstructed one where it exists, exactly as the
// Gate-decision path does. Reconstructing "wfrun-"+id is the fallback for a row whose id was never
// stamped, which the launch path does write but a Trigger-started execution stamps only after
// EnsureWorkflowRun — signalling the wrong execution id is worse than signalling none, so this
// prefers what was recorded.
func CancelWorkflowRun(ctx context.Context, temporal client.Client, workflowRunID, temporalID string) error {
	if temporalID == "" {
		temporalID = "wfrun-" + workflowRunID
	}
	if err := temporal.CancelWorkflow(ctx, temporalID, ""); err != nil {
		return fmt.Errorf("cancel workflow run %s: %w", workflowRunID, err)
	}
	return nil
}
