package orchestrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/dstout-devops/stratt/core/internal/contract"
	"github.com/dstout-devops/stratt/core/internal/policy"
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
	// LaunchParams are this Workflow's own declared inputs, resolved against its input
	// schema (defaults applied, required enforced, unknown keys REJECTED — ADR-0118 D2/D4)
	// — the source for a Step's {{.launch.x}} bindings. The gate + the launching
	// Principal's authz remain the control; these only parameterize what was already
	// declared and gated.
	LaunchParams map[string]any
	// ViewName is the View a remediation INHERITS from the Baseline that raised its Finding.
	//
	// A converge Workflow is a RECIPE — what to do — not a target. The Assignment already says
	// WHERE (`view:`), the compiler copies it onto the Baseline, and a Step re-stating it was one
	// value with two binding sites, able to disagree (§2.4). Worse, it made the recipe unusable for
	// any host outside the View its author happened to name: a host Stratt had just BUILT could not
	// be converged by it, and the only workaround was to label the host into that View — which the
	// one-owner-per-label-key rule (ADR-0041) may forbid the builder from doing.
	//
	// Empty for a direct launch, where the Steps' own declared Views are the only targets.
	ViewName string
	// EntityScope narrows every Actuator Step of this DAG to one Entity (ADR-0150 D3), set when
	// the DAG was launched to remediate a Finding. Empty ⇒ each Step converges its whole View,
	// which is every other launch path.
	EntityScope string
	// Environment is the floor's own active environment, stamped at launch and NOT asserted by
	// the launcher (ADR-0122 D2). It used to arrive inside Context as a plain string, which
	// meant a caller on a prod floor could assert `environment: dev` and walk past a prod
	// freeze window — an authorization defect that typing the string would not have fixed.
	Environment string
	// Context is what the launcher asserts about the CHANGE, which policy Steps decide on
	// (ADR-0063): changeClass, committers, plus arbitrary labels. Environment is NOT in here
	// any more (see above), and the core-owned `stratt.change/` namespace is refused from it.
	//
	// It is a SEPARATE field from LaunchParams deliberately (ADR-0118 D4). Both used to
	// ride one bag, which made the two concepts indistinguishable: closing the world over
	// a Workflow's parameters would have forced every policy-gated Workflow to declare
	// `environment` as one of its own inputs, which it is not — a Workflow does not declare
	// facts about the change being made to it. Splitting them is what lets the input schema
	// actually be closed.
	//
	// Never bound by {{.launch.x}}: a Step reads the Workflow's parameters, not the
	// change's context.
	Context map[string]any
	// ParentWorkflowRunID / ParentStepName link a NESTED execution back to the Step that
	// started it (ADR-0139 D2). Empty for a top-level launch. Carried on DAGInput because
	// RunDAG mints its own row via EnsureWorkflowRun — the trigger engine's pattern — so
	// the link has to arrive with the input rather than be stamped afterwards.
	ParentWorkflowRunID string
	ParentStepName      string
	// ResolvedCapability is set when this run was reached through a nested capability Step
	// (ADR-0139 D3) — recorded on the row so the binding that decided it is readable after
	// the fact, not only in an activity's history.
	ResolvedCapability ResolvedCapability
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
	// stepCanceled is a Step that was IN FLIGHT when the DAG was cancelled (ADR-0157 D5). Distinct
	// from stepFailed because the estate is in a different state: a failed converge reported why it
	// stopped, whereas a cancelled one may have been mid-change on a real machine. Distinct from ""
	// (never started), which is the third thing D5's summary has to be able to say.
	stepCanceled = "canceled"
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
	// THE launch-input chokepoint (ADR-0118 D4). Every transport reaches this line — the
	// API handler, MCP, the event Trigger and the schedule Trigger — so validating here is
	// what makes "one capability, one validation model" true rather than aspirational
	// (§1.6). Validating in the HTTP handler alone would leave the other three bypassing it.
	//
	// It runs as an ACTIVITY rather than inline: schema compilation is deterministic today,
	// but pinning replay determinism to a validator library's behaviour across upgrades
	// would be a latent trap, and workflow code must stay replay-safe.
	//
	// The API door ALSO validates eagerly so a human gets a 400 instead of a failed Run.
	// Same function, two call sites: one for a good error, one that nothing can skip.
	if err := workflow.ExecuteActivity(ctx, a.ResolveLaunchInputs, spec, in.LaunchParams).
		Get(ctx, &in.LaunchParams); err != nil {
		return finishWorkflowRun(ctx, a, in, types.RunFailed, nil, err)
	}
	// The change context gets the SAME treatment at the SAME chokepoint (ADR-0122): what the
	// launcher asserted is validated, and what core can establish is derived rather than
	// accepted. An activity because the derivation reads the estate's Actuator declarations to
	// learn which inputs elevate — I/O, so it cannot sit on the workflow goroutine.
	//
	// A refusal FAILS the Run rather than being dropped: the whole point is that a Run whose
	// governance inputs are wrong must not proceed with a policy decision made on them.
	if err := workflow.ExecuteActivity(ctx, a.ResolveChangeContext, spec, in.Context).
		Get(ctx, &in.Context); err != nil {
		return finishWorkflowRun(ctx, a, in, types.RunFailed, nil, err)
	}
	if err := workflow.ExecuteActivity(ctx, a.MarkWorkflowRunRunning, in.WorkflowRunID).Get(ctx, nil); err != nil {
		return finishWorkflowRun(ctx, a, in, types.RunFailed, nil, err)
	}

	state := map[string]string{} // step → "" (pending) | running | terminal
	for _, s := range spec.Steps {
		state[s.Name] = ""
	}

	// Cancellation (ADR-0157 D1/D2/D5), the same shape RunAgainstView and RunAction already use and
	// for the same reason: the Workflow is the single writer of its own terminal status (ADR-0026,
	// §2.1), so the API handler signals Temporal and never writes `canceled` itself. Two writers of
	// one terminal value fails in the ugliest direction — the handler marks canceled, cancellation
	// loses a race, and the DAG carries on against a row that says it stopped.
	//
	// Activities cannot run on a cancelled context, which is why the normal `finishWorkflowRun` on
	// the way out is a no-op here (its activity simply fails and the error is discarded) and why
	// this runs on a DISCONNECTED context. That is not a subtlety to rediscover: it is why the
	// children's handlers are written this way.
	//
	// The children are already reaped by REQUEST_CANCEL — each stamps its own status and deletes its
	// own K8s Job — so this handler owns only what nothing else can: the parent's row and its Gates.
	defer func() {
		if in.WorkflowRunID == "" || !errors.Is(ctx.Err(), workflow.ErrCanceled) {
			return
		}
		dctx, dcancel := workflow.NewDisconnectedContext(ctx)
		defer dcancel()
		dctx = workflow.WithActivityOptions(dctx, opts)
		// Gates first: a Gate left pending outlives the run in an operator's queue, whereas the
		// WorkflowRun row is merely stale until the next line. Worst-first if only one succeeds.
		_ = workflow.ExecuteActivity(dctx, a.CancelPendingGates, in.WorkflowRunID).Get(dctx, nil)
		// D5 — the per-Step map goes in exactly as a normal finish writes it, because it already
		// distinguishes the three states that matter after a cancel: terminal (the Step completed),
		// "running" (cancelled in flight, so the estate is genuinely half-applied there) and ""
		// (never started). Cancellation is not rollback and this records which is which; drift
		// detection surfaces the rest, which is the system working rather than a gap.
		_ = workflow.ExecuteActivity(dctx, a.FinishWorkflowRun, in.WorkflowRunID, types.RunCanceled, state).Get(dctx, nil)
	}()
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
			case step.Policy != nil:
				status = runPolicyStep(gctx, a, in, step)
			case step.Workflow != "":
				status = runNestedWorkflowStep(gctx, in, step, boundOutputs)
			case step.Action != "":
				status, outputs = runActionStep(gctx, in, step, boundOutputs)
			default:
				status, outputs = runActuationStep(gctx, in, step, boundOutputs)
			}
			// A Step whose goroutine finds itself on a cancelled context was in flight when the
			// cancel arrived, and is recorded as such rather than as a failure (D5). The ambiguity
			// is stated rather than hidden: a Step that genuinely failed a moment BEFORE the cancel
			// lands here too, and cannot be told apart from one the cancel stopped. Recording both
			// as `canceled` is the safer error — it says "this may have been mid-change", which is
			// the claim an operator needs to check, whereas `failed` asserts it finished failing.
			if errors.Is(gctx.Err(), workflow.ErrCanceled) {
				status = stepCanceled
			}
			done.Send(gctx, stepResult{Name: step.Name, Status: status, Outputs: outputs})
		})
	}

	// schedule marks unmet-condition Steps skipped and launches ready ones,
	// repeating until stable (a skip can cascade further skips).
	schedule := func() {
		// Nothing new starts once the DAG is cancelled. Those Steps stay "" — D5's "never started"
		// — which is both true and the useful thing to record: launching a child on a cancelled
		// context would produce a Run row that exists only to fail, and would make the summary
		// claim work was attempted where none was.
		if errors.Is(ctx.Err(), workflow.ErrCanceled) {
			return
		}
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
		if hasOutputs(r.Outputs) {
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
		// SAME TRAP AS THE NESTED CHILD BELOW, and it was live here while that one was fixed.
		// Temporal's default TERMINATES children when the parent closes — including when the
		// parent merely FAILS with this Step still in flight. A terminated RunAgainstView never
		// reaches its cancellation handler, so CleanupRun never deletes the K8s Job (it keeps
		// converging real machines after the DAG is over) and FinishRun never stamps the Run,
		// which then reads `running` forever. REQUEST_CANCEL lets the child write its own
		// terminal status, which is the single-writer rule ADR-0026 already established.
		ParentClosePolicy: enumspb.PARENT_CLOSE_POLICY_REQUEST_CANCEL,
	})
	var outcome RunOutcome
	// The Step's own View, or the one inherited from the launching Baseline. A Step that declares
	// none is not underspecified — it is saying "converge whatever this Assignment covers".
	viewName := step.ViewName
	if viewName == "" {
		viewName = in.ViewName
	}
	err := workflow.ExecuteChildWorkflow(cctx, RunAgainstView, RunInput{
		ViewName:        viewName,
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
		EntityScope:     in.EntityScope,
	}).Get(cctx, &outcome)
	if err != nil {
		return stepFailed, nil
	}
	// An ACTUATOR Step can now hand a value to a later Step, which an ACTION Step always could.
	// The asymmetry was arbitrary and it made the born-on-target CSR flow unexpressible: the target
	// generates key+CSR (an ansible Apply), the CLM signs (an Action), the certificate is written
	// back (another Apply) — and step one had nowhere to put its CSR (CERT-2).
	return stepSucceeded, outcome.Outputs
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
	var a *Activities

	// A class-named Step resolves to the bound provider's ADVERTISED implementation before
	// anything else (ADR-0140 D3 row 2). It is an ACTIVITY, not workflow-side code: resolution
	// reads the store, and a Temporal workflow function must stay deterministic — a rebind
	// between the original run and a replay would otherwise rewrite history.
	//
	// The resolved name is carried on the child RunInput ALONGSIDE the class, never instead of
	// it: the class is what the author asked for and the Action is what served it, and §1.8's
	// one-click descent needs both. Recording only the resolved name would make a Run indis-
	// tinguishable from one that named the provider directly, which is the coupling this removes.
	action := step.Action
	if step.ActionCapability != "" {
		if err := workflow.ExecuteActivity(ctx, a.ResolveActionCapability, step.ActionCapability).Get(ctx, &action); err != nil {
			return stepFailed, nil
		}
	}

	var params json.RawMessage
	// Params are validated against the CLASS Contract for a class-named Step and against the
	// Action's own for a named one — the Step must be valid independent of the binding.
	if err := workflow.ExecuteActivity(ctx, a.ResolveActionStepParams,
		action, step.ActionCapability, step.Params, in.Event, steps, in.LaunchParams).Get(ctx, &params); err != nil {
		return stepFailed, nil
	}
	cctx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowID: ChildRunID(in.WorkflowRunID, step.Name),
		// As above. RunAction has the same cancellation handler RunAgainstView does — a
		// disconnected context, CleanupRun, then FinishRun(canceled) — and TERMINATE skips all
		// of it. Actions are where the LONG runs live (a tofu apply), so this is the site where
		// an unreaped pod runs longest after the DAG it belonged to has closed.
		ParentClosePolicy: enumspb.PARENT_CLOSE_POLICY_REQUEST_CANCEL,
	})
	var outcome RunOutcome
	// The per-class capability requests ride with the Step, resolved against the same namespaces
	// its params were (so an Intent's allocation request reaches the provider through the launch
	// interface like every other declared value).
	capArgs, cerr := resolveCapabilityArgs(ctx, a, step, in, steps)
	if cerr != nil {
		return stepFailed, nil
	}
	err := workflow.ExecuteChildWorkflow(cctx, RunAction, RunInput{
		Action:           action,
		ActionCapability: step.ActionCapability,
		DryRun:           step.DryRun,
		Params:           params,
		CapabilityArgs:   capArgs,
		CredentialRefs:   step.CredentialRefs,
		Principal:        in.Principal,
		WorkflowRunID:    in.WorkflowRunID,
		StepName:         step.Name,
	}).Get(cctx, &outcome)
	if err != nil {
		return stepFailed, nil
	}
	return stepSucceeded, outcome.Outputs
}

// runNestedWorkflowStep runs another declared Workflow as a child of this one (ADR-0139 D1).
//
// It starts RunDAG — the SAME chokepoint every other launcher uses — with WorkflowRunID empty, so
// the child mints its own row via EnsureWorkflowRun and resolves its own launch inputs. That is
// the trigger engine's pattern verbatim, and choosing it over "extract the HTTP door's body" is
// the difference between reusing the guarantee and re-implementing it: a nested Step CANNOT skip
// input resolution, because the thing it starts is the thing that resolves (§1.6).
//
// Principal, environment, change context and Cell are INHERITED, never re-asserted (D6). That is a
// privilege decision, not a convenience: a child that could re-assert any of them would be a
// confused-deputy channel wearing a Workflow's clothes.
func runNestedWorkflowStep(ctx workflow.Context, in DAGInput, step types.Step, steps map[string]json.RawMessage) string {
	var a *Activities

	// The CLASS form resolves to a concrete Workflow FIRST (ADR-0139 D3), in an activity, so the
	// workflow stays deterministic and everything downstream — input validation, dispatch, descent
	// — sees a concrete name. `resolved` also travels onto the child's record: launch-time
	// resolution forfeits the readable record compile time gives, so without it the answer to
	// "why did this run compute-build?" would exist only in an activity's history.
	name := step.Workflow
	var resolved ResolvedCapability
	if step.WorkflowCapability != "" {
		if err := workflow.ExecuteActivity(ctx, a.ResolveWorkflowCapability, step.WorkflowCapability, step.ForKind).
			Get(ctx, &resolved); err != nil {
			return stepFailed
		}
		name = resolved.Workflow
	}

	// The child's inputs may bind {{.steps.x}} / {{.launch.x}} / {{.event.x}} like any other
	// Step's arguments. Resolved in an activity for the same reason Step params are.
	var inputs map[string]any
	if err := workflow.ExecuteActivity(ctx, a.ResolveNestedInputs, step.Inputs, in.Event, steps, in.LaunchParams).
		Get(ctx, &inputs); err != nil {
		return stepFailed
	}

	cctx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowID: ChildWorkflowRunID(in.WorkflowRunID, step.Name),
		// THE default is wrong here and it is one line (ADR-0139 D2). Temporal TERMINATES
		// children when the parent closes; a terminated RunDAG never reaches finishWorkflowRun,
		// so its WorkflowRun row reads `running` FOREVER and its K8s Jobs go unreaped — a record
		// that lies about a run's state, which is the §1.8 failure mode in its purest form.
		// REQUEST_CANCEL lets the child write its own terminal status, keeping the single-writer
		// rule that already governs Run status. Invisible until a parent is cancelled in anger.
		ParentClosePolicy: enumspb.PARENT_CLOSE_POLICY_REQUEST_CANCEL,
	})
	err := workflow.ExecuteChildWorkflow(cctx, RunDAG, DAGInput{
		WorkflowName: name,
		// What the Step ASKED FOR, alongside what served it (§1.8) — recorded on the child's
		// row so a capability-routed nested run is distinguishable from one that named the
		// Workflow directly.
		ResolvedCapability: resolved,
		// Inherited, never re-asserted (D6).
		Principal:    in.Principal,
		Environment:  in.Environment,
		Context:      in.Context,
		LaunchParams: inputs,
		// The nesting link (D2). Without it the child is an orphan whose existence is only
		// inferable from timing, and §1.8's descent ladder loses a rung.
		ParentWorkflowRunID: in.WorkflowRunID,
		ParentStepName:      step.Name,
	}).Get(cctx, nil)
	if err != nil {
		// D6b: the child's terminal status IS this Step's status. failed, denied and expired all
		// make the parent Step fail, so `needs:` + `when: success|failure|always` keep meaning
		// over a nested Step exactly as over any other. Left unstated, those edges would be
		// undefined the moment a Step is a subtree.
		return stepFailed
	}
	return stepSucceeded
}

// ResolvedCapability records how a nested capability Step reached its Workflow (ADR-0139 D3):
// the class asked for, the kind, the provider that won, and the Workflow it advertised. Carried
// onto the child's WorkflowRun because launch-time resolution has no compiled artifact to read
// afterwards, and "resolved at run time with no declaration anyone can read" is the worse boundary
// this repo already argues against.
type ResolvedCapability struct {
	Capability string
	ForKind    string
	Provider   string
	Workflow   string
}

// ResolveWorkflowCapability maps (class, Intent kind) to the bound provider's build Workflow. An
// activity because it reads the store, and because a rebind between the original run and a replay
// must not rewrite history.
//
// Fails NON-RETRYABLY, carrying the resolver's own reason: "no verified provider" and "two
// providers, add a capability-binding" send the reader to different places (§1.8), and neither is
// fixed by trying again.
func (a *Activities) ResolveWorkflowCapability(ctx context.Context, capClass, forKind string) (ResolvedCapability, error) {
	if a.ResolveBuildWorkflow == nil {
		return ResolvedCapability{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("step names capability %q but no build resolver is configured", capClass),
			"CapabilityResolverUnavailable", nil)
	}
	provider, wf, err := a.ResolveBuildWorkflow(ctx, capClass, forKind)
	if err != nil {
		return ResolvedCapability{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("resolve capability %q for Intent/%s: %v", capClass, forKind, err),
			"CapabilityUnresolvable", err)
	}
	return ResolvedCapability{Capability: capClass, ForKind: forKind, Provider: provider, Workflow: wf}, nil
}

// ChildWorkflowRunID is the deterministic Temporal id of a nested WorkflowRun — the parent's
// descent rung is navigable by construction (§1.8), the same property ChildRunID gives a Step's
// Run. Distinct prefix so a nested Workflow and a Step's Run can never collide on one id.
func ChildWorkflowRunID(parentWorkflowRunID, step string) string {
	return "wfnest-" + parentWorkflowRunID + "-" + step
}

// ResolveNestedInputs binds a nested Step's inputs against the DAG's namespaces. The child's own
// `inputs` SCHEMA is then applied inside RunDAG by ResolveLaunchInputs — deliberately not here, so
// there is exactly one place launch inputs are validated (ADR-0118 D4).
func (a *Activities) ResolveNestedInputs(ctx context.Context, inputs map[string]any, event map[string]any, steps map[string]json.RawMessage, launch map[string]any) (map[string]any, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	ns := template.Namespaces{"event": event, "steps": stepsNamespace(steps), "launch": launch}
	out, err := template.SubstituteParams(inputs, ns)
	if err != nil {
		return nil, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidNestedInputs", err)
	}
	return out, nil
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
	return awaitGate(ctx, a, in.WorkflowRunID, step.Name, step.Gate.Approvers, step.Gate.TimeoutSeconds, step.Gate.Threshold, planDigest)
}

// awaitGate opens a Gate record and blocks on human decision signals, racing the
// declared timeout (ADR-0011). It is the shared human-approval primitive: a
// declared Gate Step (runGateStep) and a policy REQUIRE_APPROVAL/ESCALATE
// outcome (runPolicyStep, ADR-0064) both call it, so a policy-opened Gate is
// decided through the identical API/audit path as a declared one.
//
// Quorum (ADR-0071): the Gate proceeds only after `threshold` DISTINCT authorized
// approvals (threshold < 1 ⇒ 1, the single-approval default). Each DecideGate
// call signals one authorized approval; the workflow accumulates the distinct
// principals in replay-safe state, so a re-approval by the same principal never
// double-counts. Any single DENY short-circuits to failure regardless of
// threshold; the timeout expires the Gate. Approved ⇒ the step succeeds.
func awaitGate(ctx workflow.Context, a *Activities, workflowRunID, stepName string, approvers types.GateApprovers, timeoutSeconds, threshold int, planDigest string) string {
	if threshold < 1 {
		threshold = 1
	}
	var gate types.Gate
	if err := workflow.ExecuteActivity(ctx, a.CreateGateRecord, workflowRunID, stepName, planDigest, approvers).Get(ctx, &gate); err != nil {
		return stepFailed
	}

	approvedBy := map[string]bool{} // distinct approving principals
	denied := false
	timedOut := false
	lastPrincipal, note := "", ""

	sigChan := workflow.GetSignalChannel(ctx, GateSignalName(stepName))
	var timer workflow.Future
	if timeoutSeconds > 0 {
		timer = workflow.NewTimer(ctx, time.Duration(timeoutSeconds)*time.Second)
	}
	for len(approvedBy) < threshold && !denied && !timedOut {
		sel := workflow.NewSelector(ctx)
		sel.AddReceive(sigChan, func(c workflow.ReceiveChannel, _ bool) {
			var d GateDecision
			c.Receive(ctx, &d)
			if d.Approved {
				approvedBy[d.Principal] = true
			} else {
				denied = true
			}
			lastPrincipal, note = d.Principal, d.Note
		})
		if timer != nil {
			sel.AddFuture(timer, func(workflow.Future) { timedOut = true })
		}
		// CANCELLATION HAS TO BE A BRANCH OF THE SELECT, and ADR-0157 assumed it was not needed.
		// Its Context section states "a Gate Step blocks in sel.Select(ctx); on cancellation that
		// returns" — it does NOT. A Temporal Selector unblocks only on a branch it was given, and
		// with just the signal channel and the optional timer, a cancelled DAG left this goroutine
		// blocked forever: `done` never received, `running` never reached zero, the DAG never
		// reached its cancellation handler, and the whole execution sat until its Temporal timeout.
		// So the cancel door would have returned 202 and stopped nothing — the exact "success that
		// did nothing" this ADR refuses elsewhere (D6). Found by running it, not by reading it.
		sel.AddReceive(ctx.Done(), func(workflow.ReceiveChannel, bool) {})
		sel.Select(ctx)
		if errors.Is(ctx.Err(), workflow.ErrCanceled) {
			break
		}
	}

	// CANCELLED: do not try to record anything here (ADR-0157 D2). `sel.Select` returns on
	// cancellation with neither an approval nor a denial, so the switch below would fall to its
	// default and record `expired` — which says the approval window lapsed when the truth is that
	// someone stopped the run. The activity cannot execute on a cancelled context anyway; attempting
	// it only trades a wrong record for a hung one. The DAG's cancellation handler records this Gate
	// `canceled` on a disconnected context, which is the one place that CAN.
	if errors.Is(ctx.Err(), workflow.ErrCanceled) {
		return stepFailed
	}
	status := types.GateExpired
	switch {
	case denied:
		status = types.GateDenied
	case len(approvedBy) >= threshold:
		status = types.GateApproved
	}
	if err := workflow.ExecuteActivity(ctx, a.RecordGateDecision, gate.ID, status, lastPrincipal, note).Get(ctx, nil); err != nil {
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
	wr, err := a.Store.CreateNestedWorkflowRun(ctx, in.WorkflowName, temporalID, in.Principal, in.Trigger,
		in.ParentWorkflowRunID, in.ParentStepName)
	if err != nil {
		return "", err
	}
	// The inherited View, on the Trigger/nested path too (ADR-0157 D3). The API path records it in
	// LaunchWorkflowRun; this is the other door that mints a row, and a cancel must be authorizable
	// against either. No-op when the Steps name their own Views.
	if err := a.Store.SetWorkflowRunView(ctx, wr.ID, in.ViewName); err != nil {
		return "", err
	}
	// The binding that decided this run (ADR-0139 D3). Launch-time resolution has no compiled
	// artifact to read afterwards, so without this the answer to "why did this run compute-build?"
	// lives only in an activity's history — the "resolved at run time with no declaration anyone
	// can read" boundary this repo already argues against.
	if r := in.ResolvedCapability; r.Capability != "" {
		if err := a.Store.SetWorkflowRunStatus(ctx, wr.ID, types.RunPending, map[string]any{
			"resolvedCapability": r.Capability,
			"resolvedForKind":    r.ForKind,
			"resolvedProvider":   r.Provider,
		}); err != nil {
			return "", err
		}
	}
	return wr.ID, nil
}

// hasOutputs reports whether a Step actually published something bindable.
//
// The literal `null` is NOT something: a nil json.RawMessage crosses Temporal's JSON converter as
// the four bytes "null", which every len() check reads as present. Recorded as a Step output it
// then poisons the namespace — a binding into it fails with `"<field>" is not an object`, a message
// about the consumer that says nothing about the Step which published nothing. Distinguishing
// "published nothing" from "published a value" is the whole difference between an ordinary Run and
// a defect, so it is decided in ONE place rather than at each len() call site (§1.8).
func hasOutputs(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
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

// ResolveLaunchInputs applies a Workflow's declared input defaults to the launch params and
// validates the result against its schema (ADR-0118 D2/D4) — the chokepoint RunDAG calls
// before any Step runs, so no transport can supply params the declaration forbids.
//
// A violation is NON-RETRYABLE: bad launch params are a data error, and the same body will
// fail identically three attempts later (the ADR-0024 D6 discipline — a poison input must
// not loop).
func (a *Activities) ResolveLaunchInputs(_ context.Context, spec types.Workflow, supplied map[string]any) (map[string]any, error) {
	resolved, err := contract.ResolveLaunchInputs(spec.Name, spec.Inputs, supplied)
	if err != nil {
		return nil, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidLaunchInputs", err)
	}
	return resolved, nil
}

// ResolveChangeContext admits what a launcher asserted about the change and derives what core can
// establish itself (ADR-0122) — the change-context half of the ADR-0118 D4 chokepoint, called
// immediately after ResolveLaunchInputs so every transport gets the same admission.
//
// Two kinds of work, deliberately in one activity because they answer one question:
//
//   - VALIDATE the asserted set: an unknown `changeClass` is refused rather than coerced (an
//     unknown class makes the Controls keyed on the intended one silently not fire — fail-open),
//     and an asserted `environment` or `stratt.change/*` key is refused outright because those are
//     core's to establish, not a launcher's to claim.
//   - DERIVE the `stratt.change/privileged` label from the Workflow's own Steps and their
//     Actuators' declared `elevatedInputs`. This is what makes ADR-0117 D1's typed `become`
//     Control-gateable without core learning any tool's field names.
//
// NON-RETRYABLE on a violation, like its sibling: a rejected governance assertion is a data error
// and the same body fails identically three attempts later (ADR-0024 D6).
//
// A missing Actuator declaration is NOT an error here — a Step naming an unknown Actuator fails at
// dispatch with a better message, and turning that into a change-context failure would report the
// wrong cause (§1.8). It simply derives nothing for that Step.
func (a *Activities) ResolveChangeContext(ctx context.Context, spec types.Workflow, supplied map[string]any) (map[string]any, error) {
	if err := policy.ValidateChangeContext(supplied); err != nil {
		return nil, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidChangeContext", err)
	}
	out := make(map[string]any, len(supplied)+1)
	for k, v := range supplied {
		out[k] = v
	}
	if a == nil || a.Store == nil {
		// No store ⇒ no Actuator declarations to read, so nothing is derivable. This is the
		// test harness, which registers this activity FOR REAL so every DAG test walks the
		// admission half rather than mocking past it — the half that refuses a spoofed
		// environment or a bogus change class, and the half that must never be skippable.
		// Production always has a store: RunDAG's first activity is LoadWorkflow, which reads
		// it, so a nil store fails long before here.
		return out, nil
	}
	acts, err := a.Store.ListActuators(ctx)
	if err != nil {
		// Retryable on purpose: a store blip is not a governance verdict. Failing OPEN here
		// would silently drop the privileged label and with it the Control that gates on it.
		return nil, err
	}
	elevatedBy := make(map[string][]string, len(acts))
	for _, act := range acts {
		if len(act.ElevatedInputs) > 0 {
			elevatedBy[act.Name] = act.ElevatedInputs
		}
	}
	for k, v := range policy.DeriveElevation(spec.Steps, elevatedBy) {
		out[k] = v
	}
	return out, nil
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
	raw, err := contract.ResolveActuatorParamsFor(name, a.Store.PluginIdentityOf(ctx, name), params, ns)
	if err != nil {
		return nil, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidStepParams", err)
	}
	return raw, nil
}

// ResolveActionStepParams is the Action counterpart: substitute event/steps
// bindings and re-validate against the Action's input Contract (§2.2, ADR-0031).
func (a *Activities) ResolveActionStepParams(ctx context.Context, action, capClass string, params map[string]any, event map[string]any, steps map[string]json.RawMessage, launch map[string]any) (json.RawMessage, error) {
	ns := template.Namespaces{"event": event, "steps": stepsNamespace(steps), "launch": launch}
	resolve := func() (json.RawMessage, error) { return contract.ResolveActionParams(action, params, ns) }
	if capClass != "" {
		// The CLASS Contract governs a class-named Step, not the resolved provider's own
		// (ADR-0112 D2, applied to the Step form): the author wrote provider-agnostic params,
		// and checking them against whichever provider is bound would make the Step's validity
		// depend on the binding — which is exactly what naming a class is meant to avoid.
		resolve = func() (json.RawMessage, error) { return contract.ResolveCapabilityParams(capClass, params, ns) }
	}
	raw, err := resolve()
	if err != nil {
		return nil, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidStepParams", err)
	}
	return raw, nil
}

// ResolveActuatorCapability maps a capability CLASS to the bound provider's ACTUATOR name
// (ADR-0140 D4) — the Actuator-shaped sibling of ResolveActionCapability, for a Baseline that
// names a class. An activity for the same two reasons: it reads the store, and a workflow function
// must stay deterministic across replay.
func (a *Activities) ResolveActuatorCapability(ctx context.Context, capClass string) (string, error) {
	if a.ResolveActuator == nil {
		return "", temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("declaration names capability %q but no capability resolver is configured", capClass),
			"CapabilityResolverUnavailable", nil)
	}
	name, err := a.ResolveActuator(ctx, capClass)
	if err != nil {
		return "", temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("resolve actuator capability %q: %v", capClass, err), "CapabilityUnresolvable", err)
	}
	return name, nil
}

// ResolveActionCapability maps a capability CLASS to the bound provider's advertised
// implementation (ADR-0140 D1/D3 row 2). An activity because it reads the store, and because a
// workflow function must not — a rebind between run and replay would rewrite history.
//
// Fails NON-RETRYABLY and visibly: unresolvable means no verified provider, an ambiguous pair, or
// a provider that advertised no implementation, and none of those is fixed by trying again (§1.8).
func (a *Activities) ResolveActionCapability(ctx context.Context, capClass string) (string, error) {
	if a.ResolveCapability == nil {
		return "", temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("step names capability %q but no capability resolver is configured", capClass),
			"CapabilityResolverUnavailable", nil)
	}
	action, err := a.ResolveCapability(ctx, capClass)
	if err != nil {
		return "", temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("resolve capability %q: %v", capClass, err), "CapabilityUnresolvable", err)
	}
	return action, nil
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

// CancelPendingGates records every still-pending Gate of a cancelled WorkflowRun as `canceled`
// (ADR-0157 D2). Without it a cancelled DAG leaves a Gate PENDING FOREVER — an approval an operator
// can still act on, for a workflow that is gone — because the Gate Step's own RecordGateDecision
// runs on the cancelled context and cannot execute.
//
// ONE activity over the set rather than one per Gate, and the reason is the failure mode: the
// caller is a cancellation handler on a disconnected context, and a partial sweep there would leave
// SOME gates pending with nothing left running to finish them. A single activity either records
// them all or retries as a unit.
//
// `decidedBy` is deliberately empty. Nobody decided — the run was stopped — and writing the
// cancelling Principal into a field that means "who approved or denied this" would put a wrong
// answer in the audit record, which is the same §1.8 line D2 draws between `canceled` and `expired`.
// The cancelling Principal is recorded on the WorkflowRun, where it belongs.
func (a *Activities) CancelPendingGates(ctx context.Context, workflowRunID string) error {
	gates, err := a.Store.ListGatesForWorkflowRun(ctx, workflowRunID)
	if err != nil {
		return err
	}
	for _, g := range gates {
		if g.Status != types.GatePending {
			continue // already approved, denied or expired — a decision that really happened
		}
		if err := a.Store.DecideGate(ctx, g.ID, types.GateCanceled, "", "the WorkflowRun was cancelled"); err != nil {
			return err
		}
	}
	return nil
}

// FinishWorkflowRun records the terminal status and per-Step outcomes.
func (a *Activities) FinishWorkflowRun(ctx context.Context, id string, status types.RunStatus, steps map[string]string) error {
	summary := map[string]any{}
	if steps != nil {
		summary["steps"] = steps
	}
	return a.Store.SetWorkflowRunStatus(ctx, id, status, summary)
}

// ResolveCapabilityArgs substitutes a Step's per-class capability requests against the same
// namespaces its params use, so an Intent's allocation request reaches the provider through the
// launch interface like every other declared value.
//
// Deliberately NOT validated here: each block is checked against `capabilities/<class>.input` in
// resolveCapabilities, which is the one place that knows which class it belongs to. Validating
// early would mean this function learning the class map, which is core learning content (§1.5).
func (a *Activities) ResolveCapabilityArgs(
	_ context.Context, args map[string]map[string]any, event map[string]any,
	steps map[string]json.RawMessage, launch map[string]any,
) (map[string]map[string]any, error) {
	if len(args) == 0 {
		return nil, nil
	}
	ns := template.Namespaces{"event": event, "steps": stepsNamespace(steps), "launch": launch}
	out := make(map[string]map[string]any, len(args))
	for class, req := range args {
		resolved, err := template.SubstituteParams(req, ns)
		if err != nil {
			return nil, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("capability %q request: %v", class, err), "InvalidCapabilityArgs", err)
		}
		out[class] = resolved
	}
	return out, nil
}

// resolveCapabilityArgs runs the substitution as an activity so the workflow stays deterministic.
func resolveCapabilityArgs(
	ctx workflow.Context, a *Activities, step types.Step, in DAGInput, steps map[string]json.RawMessage,
) (map[string]map[string]any, error) {
	if len(step.CapabilityArgs) == 0 {
		return nil, nil
	}
	var out map[string]map[string]any
	err := workflow.ExecuteActivity(ctx, a.ResolveCapabilityArgs,
		step.CapabilityArgs, in.Event, steps, in.LaunchParams).Get(ctx, &out)
	return out, err
}
