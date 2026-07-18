package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
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
	// LaunchParams are the operator-supplied inputs from POST /workflows/{name}/runs
	// — the source for a Step's {{.launch.x}} bindings (ADR-0059 re-placement /
	// per-instance builds). The gate + the launching Principal's authz remain the
	// control; these only parameterize what was already declared and gated.
	LaunchParams map[string]any
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
	// Outputs are an Action Step's typed outputs (ADR-0031), accumulated into
	// the DAG's steps namespace for downstream {{.steps.<name>.outputs.x}} binds.
	Outputs json.RawMessage
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
	// stepOutputs accumulates completed Action Steps' typed outputs, the source
	// of the {{.steps.<name>.outputs.x}} binding namespace (ADR-0031). Written
	// only on the main workflow goroutine (on done.Receive), so it is safe to
	// read when launching a downstream Step.
	stepOutputs := map[string]json.RawMessage{}

	done := workflow.NewChannel(ctx)
	running := 0
	launch := func(step types.Step) {
		state[step.Name] = "running"
		running++
		boundOutputs := copyOutputs(stepOutputs) // snapshot for this goroutine
		workflow.Go(ctx, func(gctx workflow.Context) {
			var status string
			var outputs json.RawMessage
			switch {
			case step.Gate != nil:
				status = runGateStep(gctx, a, in, step, boundOutputs)
			case step.Action != "":
				status, outputs = runActionStep(gctx, in, step, boundOutputs)
			default:
				status, outputs = runActuationStep(gctx, in, step, boundOutputs)
			}
			done.Send(gctx, stepResult{Name: step.Name, Status: status, Outputs: outputs})
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
		if len(r.Outputs) > 0 {
			stepOutputs[r.Name] = r.Outputs
		}
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

// copyOutputs snapshots the accumulated step-outputs map so a launched Step's
// goroutine binds against a stable view (the map keeps mutating as later Steps
// complete). Determinism-safe: it copies workflow state, no I/O.
func copyOutputs(m map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// runActuationStep executes one Step as a child RunAgainstView workflow.
// EnsureRun (slice 6) creates the Run row, stamping WorkflowRunID/StepName. A Plan
// Step (step.Plan) instead runs the PLAN verb and RETURNS its digest as the Step's
// output (planDigest), the pin a downstream Gate binds and a plan-pinned Apply
// consumes (ADR-0047 §8). A plan-pinned Apply (step.PlanFrom) reads that digest
// from core-held Step state and threads it into the child Run.
func runActuationStep(ctx workflow.Context, in DAGInput, step types.Step, steps map[string]json.RawMessage) (string, json.RawMessage) {
	// Resolve the Step's {{.event.x}}/{{.steps.x}} param bindings and re-validate
	// against the Actuator Contract in an activity (ADR-0024/0031). A binding to
	// a missing field or a contract violation fails the Step visibly (§1.8),
	// never reaching the Actuator.
	var params json.RawMessage
	var a *Activities
	if err := workflow.ExecuteActivity(ctx, a.ResolveStepParams, step.Actuator, step.Params, in.Event, steps, in.LaunchParams).Get(ctx, &params); err != nil {
		return stepFailed, nil
	}

	// Plan verb: produce the hash-pinned saved plan and surface its digest as this
	// Step's output. The core content-addresses + stores the plan (host.Plan); only
	// the digest flows into the DAG's step state (never the secret-bearing plan).
	if step.Plan {
		var digest string
		if err := workflow.ExecuteActivity(ctx, a.PlanStep, RunInput{
			Actuator: step.Actuator, Params: params, CredentialRefs: step.CredentialRefs,
			Principal: in.Principal, WorkflowRunID: in.WorkflowRunID, StepName: step.Name, Plan: true,
		}).Get(ctx, &digest); err != nil {
			return stepFailed, nil
		}
		out, _ := json.Marshal(map[string]string{"planDigest": digest})
		return stepSucceeded, out
	}

	// A plan-pinned Apply reads its Gate-approved digest from core-held Step state.
	planDigest := ""
	if step.PlanFrom != "" {
		planDigest = digestFromStep(steps, step.PlanFrom)
	}
	cctx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowID: ChildRunID(in.WorkflowRunID, step.Name),
	})
	err := workflow.ExecuteChildWorkflow(cctx, RunAgainstView, RunInput{
		ViewName:        step.ViewName,
		Actuator:        step.Actuator,
		Params:          params,
		Slices:          step.Slices,
		CredentialRefs:  step.CredentialRefs,
		Principal:       in.Principal,
		WorkflowRunID:   in.WorkflowRunID,
		StepName:        step.Name,
		PlanFrom:        step.PlanFrom,
		PlanDigest:      planDigest,
		FacetWriteScope: step.FacetWriteScope,
	}).Get(cctx, nil)
	if err != nil {
		return stepFailed, nil
	}
	return stepSucceeded, nil
}

// digestFromStep reads the planDigest output a Plan Step recorded into the DAG's
// step state (core-held; never a plugin re-resolve, ADR-0047 §8).
func digestFromStep(steps map[string]json.RawMessage, planStep string) string {
	raw, ok := steps[planStep]
	if !ok {
		return ""
	}
	var out struct {
		PlanDigest string `json:"planDigest"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.PlanDigest
}

// runActionStep executes one Step as a child RunAction workflow (§2.2,
// ADR-0031) and returns its typed outputs for downstream binding.
func runActionStep(ctx workflow.Context, in DAGInput, step types.Step, steps map[string]json.RawMessage) (string, json.RawMessage) {
	var params json.RawMessage
	var a *Activities
	if err := workflow.ExecuteActivity(ctx, a.ResolveActionStepParams, step.Action, step.Params, in.Event, steps, in.LaunchParams).Get(ctx, &params); err != nil {
		return stepFailed, nil
	}
	cctx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowID: ChildRunID(in.WorkflowRunID, step.Name),
	})
	var outcome RunOutcome
	err := workflow.ExecuteChildWorkflow(cctx, RunAction, RunInput{
		Action:         step.Action,
		DryRun:         step.DryRun,
		Params:         params,
		CredentialRefs: step.CredentialRefs,
		Principal:      in.Principal,
		WorkflowRunID:  in.WorkflowRunID,
		StepName:       step.Name,
	}).Get(cctx, &outcome)
	if err != nil {
		return stepFailed, nil
	}
	return stepSucceeded, outcome.Outputs
}

// runGateStep opens a Gate row and waits for an authorized decision signal
// (or the declared timeout). The workflow is the row's single writer after
// creation, so the §1.8 history shows every transition.
func runGateStep(ctx workflow.Context, a *Activities, in DAGInput, step types.Step, steps map[string]json.RawMessage) string {
	// A Gate guarding a plan-pinned Apply BINDS the exact plan digest it approves
	// (write-once, approve-what-you-see — ADR-0047 §8), read from the Plan Step's
	// output. "" for an ordinary Gate.
	planDigest := ""
	if step.PlanFrom != "" {
		planDigest = digestFromStep(steps, step.PlanFrom)
	}
	var gate types.Gate
	if err := workflow.ExecuteActivity(ctx, a.CreateGateRecord, in.WorkflowRunID, step.Name, planDigest, step.Gate.Approvers).Get(ctx, &gate); err != nil {
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

// stepsNamespace turns accumulated Step outputs (stepName → outputs JSON) into
// the template namespace backing {{.steps.<name>.outputs.<field>}} (ADR-0031).
func stepsNamespace(steps map[string]json.RawMessage) map[string]any {
	ns := make(map[string]any, len(steps))
	for name, raw := range steps {
		var out any
		if json.Unmarshal(raw, &out) == nil {
			ns[name] = map[string]any{"outputs": out}
		}
	}
	return ns
}

// ResolveStepParams substitutes a Step's {{.event.x}} / {{.steps.x}} bindings
// (the firing event and prior Steps' outputs), then re-validates the resolved
// params against the Actuator's input Contract before dispatch (ADR-0024/0031).
func (a *Activities) ResolveStepParams(ctx context.Context, actuator string, params map[string]any, event map[string]any, steps map[string]json.RawMessage, launch map[string]any) (json.RawMessage, error) {
	// A Workflow actuation Step names its Actuator explicitly (no platform default,
	// ADR-0046); validated at Workflow declaration, so empty here is a bug.
	name := actuator
	if name == "" {
		return nil, fmt.Errorf("workflow actuation step requires an explicit actuator (no platform default)")
	}
	ns := template.Namespaces{"event": event, "steps": stepsNamespace(steps), "launch": launch}
	raw, err := contract.ResolveActuatorParams(name, params, ns)
	if err != nil {
		return nil, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidStepParams", err)
	}
	return raw, nil
}

// ResolveActionStepParams is the Action counterpart: substitute event/steps
// bindings and re-validate against the Action's input Contract (§2.2, ADR-0031).
func (a *Activities) ResolveActionStepParams(ctx context.Context, action string, params map[string]any, event map[string]any, steps map[string]json.RawMessage, launch map[string]any) (json.RawMessage, error) {
	ns := template.Namespaces{"event": event, "steps": stepsNamespace(steps), "launch": launch}
	raw, err := contract.ResolveActionParams(action, params, ns)
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
// (workflowRun, step) across activity retries) and emits a gate.pending Notice
// so approvers are reached (ADR-0027). The gate id is stable across retries,
// so NoticeHash dedups the publish.
func (a *Activities) CreateGateRecord(ctx context.Context, workflowRunID, step, planDigest string, approvers types.GateApprovers) (types.Gate, error) {
	gate, err := a.Store.CreateGate(ctx, workflowRunID, step, planDigest, approvers)
	if err != nil {
		return gate, err
	}
	n := types.Notice{Kind: types.NoticeGatePending, Subject: gate.ID, Payload: map[string]any{
		"workflowRun": workflowRunID,
		"step":        step,
	}}
	// approve-what-you-see (§1.8/§1.6): the exact plan digest reaches the approver —
	// human via the notice/inbox and an agent approver via the same API/MCP payload.
	if gate.PlanDigest != "" {
		n.Payload["planDigest"] = gate.PlanDigest
	}
	if len(approvers.Principals) > 0 {
		n.Payload["approverPrincipals"] = approvers.Principals
	}
	if len(approvers.Teams) > 0 {
		n.Payload["approverTeams"] = approvers.Teams
	}
	if err := a.Bus.PublishNotice(ctx, n); err != nil {
		return gate, err
	}
	return gate, nil
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
