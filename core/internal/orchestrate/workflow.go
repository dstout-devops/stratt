package orchestrate

import (
	"context"
	"encoding/json"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/dstout-devops/stratt/core/internal/contract"
	"github.com/dstout-devops/stratt/core/internal/template"
	"github.com/dstout-devops/stratt/types"
)

// DAGInput starts one WorkflowRun: the execution of a declared Workflow
// (charter §2: Temporal-backed DAG of Steps with Gates; ADR-0011).
type DAGInput struct {
	// WorkflowRunID is the pre-created execution row for API launches.
	// Empty for Trigger-started executions: EnsureWorkflowRun creates the
	// row (the ADR-0010 pattern, ported for ADR-0018).
	WorkflowRunID string
	WorkflowName  string
	// Principal is the launching identity; every actuation Step's
	// credential `use` check runs against it (§2.5), exactly as if the
	// Principal had started each Run directly.
	Principal string
	// Trigger names the Trigger that fired this execution; empty for
	// API launches (§1.8 descent: Trigger → WorkflowRun).
	Trigger string
	// Event is the Emitter-event payload that fired this execution (empty
	// for schedule/API launches) — the source for a Step's {{.event.x}}
	// param bindings (ADR-0024).
	Event map[string]any
}

// GateDecision is the signal payload an authorized Principal sends to a
// pending Gate (via the API, which enforces the approver policy first).
type GateDecision struct {
	Approved  bool
	Principal string
	Note      string
}

// GateSignalName is the per-Step signal channel a Gate waits on.
func GateSignalName(step string) string { return "gate:" + step }

// ChildRunID is the deterministic Temporal workflow id of one Step's Run —
// the Workflow → Run descent rung is navigable by construction (§1.8).
func ChildRunID(workflowRunID, step string) string {
	return "wfrun-" + workflowRunID + "-" + step
}

// Step outcomes tracked by the DAG walk. Succeeded/failed mirror Run
// statuses; skipped means the Step's when-condition was not met.
const (
	stepSucceeded = "succeeded"
	stepFailed    = "failed"
	stepSkipped   = "skipped"
)

type stepResult struct {
	Name   string
	Status string
}

// RunDAG executes a declared Workflow: Steps launch as soon as their needs
// are terminal and their when-condition holds (independent branches never
// block each other — a pending Gate on one branch must not stall another,
// §1.8: every wait is visible as exactly what it is). Actuation Steps run as
// child RunAgainstView workflows (one Run row each, the full slice-3/4
// machinery); Gate Steps wait on a decision signal.
func RunDAG(ctx workflow.Context, in DAGInput) error {
	opts := workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	}
	ctx = workflow.WithActivityOptions(ctx, opts)
	var a *Activities

	// Trigger-started executions have no API handler to pre-create the
	// execution row — the Workflow owns it (ADR-0018, the ADR-0010 pattern).
	if in.WorkflowRunID == "" {
		wfID := workflow.GetInfo(ctx).WorkflowExecution.ID
		if err := workflow.ExecuteActivity(ctx, a.EnsureWorkflowRun, in, wfID).Get(ctx, &in.WorkflowRunID); err != nil {
			return err
		}
	}

	// The spec is pinned into workflow state here: a Git update mid-flight
	// changes future WorkflowRuns, never this one.
	var spec types.Workflow
	if err := workflow.ExecuteActivity(ctx, a.LoadWorkflow, in.WorkflowName).Get(ctx, &spec); err != nil {
		return finishWorkflowRun(ctx, a, in, types.RunFailed, nil, err)
	}
	if err := workflow.ExecuteActivity(ctx, a.MarkWorkflowRunRunning, in.WorkflowRunID).Get(ctx, nil); err != nil {
		return finishWorkflowRun(ctx, a, in, types.RunFailed, nil, err)
	}

	state := map[string]string{} // step → "" (pending) | running | terminal
	for _, s := range spec.Steps {
		state[s.Name] = ""
	}

	done := workflow.NewChannel(ctx)
	running := 0
	launch := func(step types.Step) {
		state[step.Name] = "running"
		running++
		workflow.Go(ctx, func(gctx workflow.Context) {
			var status string
			if step.Gate != nil {
				status = runGateStep(gctx, a, in, step)
			} else {
				status = runActuationStep(gctx, in, step)
			}
			done.Send(gctx, stepResult{Name: step.Name, Status: status})
		})
	}

	// schedule marks unmet-condition Steps skipped and launches ready ones,
	// repeating until stable (a skip can cascade further skips).
	schedule := func() {
		for changed := true; changed; {
			changed = false
			for _, s := range spec.Steps {
				if state[s.Name] != "" {
					continue
				}
				ready, met := stepEligible(s, state)
				if !ready {
					continue
				}
				if !met {
					state[s.Name] = stepSkipped
					changed = true
					continue
				}
				launch(s)
			}
		}
	}

	schedule()
	for running > 0 {
		var r stepResult
		done.Receive(ctx, &r)
		running--
		state[r.Name] = r.Status
		schedule()
	}

	// Raw outcomes decide the terminal status (§1.8: a failure that a
	// cleanup branch handled is still a failure on the record).
	status := types.RunSucceeded
	for _, s := range state {
		if s == stepFailed {
			status = types.RunFailed
		}
	}
	return finishWorkflowRun(ctx, a, in, status, state, nil)
}

// stepEligible reports whether all needs are terminal (ready) and, if so,
// whether the when-condition holds (met). Success (default) requires every
// need succeeded; failure requires at least one failed; always runs on any
// terminal outcome. A skipped need satisfies neither success nor failure —
// skips cascade down success chains.
func stepEligible(s types.Step, state map[string]string) (ready, met bool) {
	failed, succeeded := 0, 0
	for _, n := range s.Needs {
		switch state[n] {
		case "", "running":
			return false, false
		case stepFailed:
			failed++
		case stepSucceeded:
			succeeded++
		}
	}
	switch s.When {
	case types.WhenAlways:
		return true, true
	case types.WhenFailure:
		return true, failed > 0
	default: // success
		return true, succeeded == len(s.Needs)
	}
}

// runActuationStep executes one Step as a child RunAgainstView workflow.
// EnsureRun (slice 6) creates the Run row, stamping WorkflowRunID/StepName.
func runActuationStep(ctx workflow.Context, in DAGInput, step types.Step) string {
	// Resolve the Step's {{.event.x}} param bindings against the firing
	// event and re-validate the result against the Actuator Contract, in an
	// activity (I/O-free but not workflow-deterministic; ADR-0024). A binding
	// that references a missing field or resolves to a contract violation
	// fails the Step visibly (§1.8), never reaching the Actuator.
	var params json.RawMessage
	var a *Activities
	if err := workflow.ExecuteActivity(ctx, a.ResolveStepParams, step.Actuator, step.Params, in.Event).Get(ctx, &params); err != nil {
		return stepFailed
	}
	cctx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowID: ChildRunID(in.WorkflowRunID, step.Name),
	})
	err := workflow.ExecuteChildWorkflow(cctx, RunAgainstView, RunInput{
		ViewName:       step.ViewName,
		Actuator:       step.Actuator,
		Params:         params,
		Slices:         step.Slices,
		CredentialRefs: step.CredentialRefs,
		Principal:      in.Principal,
		WorkflowRunID:  in.WorkflowRunID,
		StepName:       step.Name,
	}).Get(cctx, nil)
	if err != nil {
		return stepFailed
	}
	return stepSucceeded
}

// runGateStep opens a Gate row and waits for an authorized decision signal
// (or the declared timeout). The workflow is the row's single writer after
// creation, so the §1.8 history shows every transition.
func runGateStep(ctx workflow.Context, a *Activities, in DAGInput, step types.Step) string {
	var gate types.Gate
	if err := workflow.ExecuteActivity(ctx, a.CreateGateRecord, in.WorkflowRunID, step.Name, step.Gate.Approvers).Get(ctx, &gate); err != nil {
		return stepFailed
	}

	decision := GateDecision{}
	decided := false
	sel := workflow.NewSelector(ctx)
	sel.AddReceive(workflow.GetSignalChannel(ctx, GateSignalName(step.Name)), func(c workflow.ReceiveChannel, _ bool) {
		c.Receive(ctx, &decision)
		decided = true
	})
	if step.Gate.TimeoutSeconds > 0 {
		sel.AddFuture(workflow.NewTimer(ctx, time.Duration(step.Gate.TimeoutSeconds)*time.Second), func(workflow.Future) {})
	}
	sel.Select(ctx)

	status := types.GateExpired
	if decided {
		status = types.GateDenied
		if decision.Approved {
			status = types.GateApproved
		}
	}
	if err := workflow.ExecuteActivity(ctx, a.RecordGateDecision, gate.ID, status, decision.Principal, decision.Note).Get(ctx, nil); err != nil {
		return stepFailed
	}
	if status == types.GateApproved {
		return stepSucceeded
	}
	return stepFailed
}

func finishWorkflowRun(ctx workflow.Context, a *Activities, in DAGInput, status types.RunStatus, steps map[string]string, cause error) error {
	_ = workflow.ExecuteActivity(ctx, a.FinishWorkflowRun, in.WorkflowRunID, status, steps).Get(ctx, nil)
	return cause
}

// ── activities ───────────────────────────────────────────────────────────────

// EnsureWorkflowRun creates the execution row for a Trigger-started RunDAG
// (ADR-0018): API launches pre-create theirs in the handler. Returns the id.
func (a *Activities) EnsureWorkflowRun(ctx context.Context, in DAGInput, temporalID string) (string, error) {
	if _, err := a.Store.GetWorkflow(ctx, in.WorkflowName); err != nil {
		return "", temporal.NewNonRetryableApplicationError(err.Error(), "WorkflowNotFound", err)
	}
	wr, err := a.Store.CreateWorkflowRun(ctx, in.WorkflowName, temporalID, in.Principal, in.Trigger)
	if err != nil {
		return "", err
	}
	return wr.ID, nil
}

// ResolveStepParams substitutes a Step's {{.event.x}} bindings against the
// firing event, then re-validates the resolved params against the Actuator's
// input Contract before dispatch (ADR-0024). Returns the resolved params as
// the JSON the Actuator receives.
func (a *Activities) ResolveStepParams(ctx context.Context, actuator string, params map[string]any, event map[string]any) (json.RawMessage, error) {
	name := actuator
	if name == "" {
		name = "ansible"
	}
	raw, err := contract.ResolveActuatorParams(name, params, template.Namespaces{"event": event})
	if err != nil {
		return nil, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidStepParams", err)
	}
	return raw, nil
}

// LoadWorkflow reads the declared Workflow spec.
func (a *Activities) LoadWorkflow(ctx context.Context, name string) (types.Workflow, error) {
	w, err := a.Store.GetWorkflow(ctx, name)
	if err != nil {
		return w, temporal.NewNonRetryableApplicationError(err.Error(), "WorkflowNotFound", err)
	}
	return w, nil
}

// MarkWorkflowRunRunning transitions the execution record to running.
func (a *Activities) MarkWorkflowRunRunning(ctx context.Context, id string) error {
	return a.Store.SetWorkflowRunStatus(ctx, id, types.RunRunning, nil)
}

// CreateGateRecord opens the pending approval row (idempotent per
// (workflowRun, step) across activity retries).
func (a *Activities) CreateGateRecord(ctx context.Context, workflowRunID, step string, approvers types.GateApprovers) (types.Gate, error) {
	return a.Store.CreateGate(ctx, workflowRunID, step, approvers)
}

// RecordGateDecision writes the terminal Gate state — approver identity and
// note are the audit trail (§1.6).
func (a *Activities) RecordGateDecision(ctx context.Context, gateID, status, decidedBy, note string) error {
	return a.Store.DecideGate(ctx, gateID, status, decidedBy, note)
}

// FinishWorkflowRun records the terminal status and per-Step outcomes.
func (a *Activities) FinishWorkflowRun(ctx context.Context, id string, status types.RunStatus, steps map[string]string) error {
	summary := map[string]any{}
	if steps != nil {
		summary["steps"] = steps
	}
	return a.Store.SetWorkflowRunStatus(ctx, id, status, summary)
}
