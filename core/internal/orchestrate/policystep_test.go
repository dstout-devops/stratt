package orchestrate

import (
	"reflect"
	"testing"
	"time"

	"github.com/dstout-devops/stratt/types"
)

func approvalControl(outcome string, teams ...string) types.Control {
	tt := make([]any, len(teams))
	for i, t := range teams {
		tt[i] = t
	}
	return types.Control{
		ID: "approve", When: "ctx.environment == 'prod'", Outcome: outcome,
		Obligations: []types.Obligation{{Type: types.ObligationRequireApproval, Params: map[string]any{"teams": tt}}},
	}
}

// approversFromDecision extracts the require_approval obligation's approvers,
// tolerating the []any that JSON-decoded estate produces.
func TestApproversFromDecision(t *testing.T) {
	dec := types.Decision{Outcome: types.OutcomeRequireApproval, Obligations: []types.Obligation{
		{Type: types.ObligationRequireApproval, Params: map[string]any{
			"teams": []any{"platform"}, "principals": []string{"alice"}, "timeoutSeconds": float64(300),
		}},
	}}
	ap, timeout, ok := approversFromDecision(dec)
	if !ok || len(ap.Teams) != 1 || ap.Teams[0] != "platform" || len(ap.Principals) != 1 || timeout != 300 {
		t.Fatalf("got approvers=%+v timeout=%d ok=%v", ap, timeout, ok)
	}
	// No approver obligation ⇒ not ok (caller fails closed).
	if _, _, ok := approversFromDecision(types.Decision{Outcome: types.OutcomeRequireApproval}); ok {
		t.Fatal("a require_approval with no approver obligation must be unsatisfiable")
	}
}

// policyAuditEvent maps each outcome to a durable audit record (ADR-0065).
func TestPolicyAuditEvent(t *testing.T) {
	arg := PolicyEvalArg{WorkflowRunID: "wr-1", StepName: "guard",
		Context: types.ChangeContext{Actor: types.PrincipalRef{ID: "alice"}}}
	cases := map[string]string{
		types.OutcomeAllow:           types.AuditOK,
		types.OutcomeDeny:            types.AuditDenied,
		types.OutcomeRequireApproval: types.OutcomeRequireApproval,
		types.OutcomeEscalate:        types.OutcomeEscalate,
	}
	for outcome, wantOutcome := range cases {
		ev := policyAuditEvent(arg, types.Decision{Outcome: outcome,
			Reasons: []types.Reason{{Code: "fired", ControlID: "c1"}}})
		if ev.Action != types.AuditPolicyDecision {
			t.Fatalf("%s: action %q", outcome, ev.Action)
		}
		if ev.Outcome != wantOutcome {
			t.Fatalf("%s: audit outcome %q want %q", outcome, ev.Outcome, wantOutcome)
		}
		if ev.PrincipalID != "alice" || ev.Object != "wr-1" {
			t.Fatalf("%s: principal/object %q/%q", outcome, ev.PrincipalID, ev.Object)
		}
		if len(ev.Detail) == 0 {
			t.Fatalf("%s: detail must carry the reasons", outcome)
		}
	}
}

// A require_approval outcome opens a Gate; an approve signal succeeds the step
// and downstream runs (ADR-0064).
func TestRunDAG_PolicyRequiresApproval_Approved(t *testing.T) {
	spec := types.Workflow{Name: "guarded", Steps: []types.Step{
		{Name: "guard", Policy: &types.PolicySpec{Controls: []types.Control{
			approvalControl(types.OutcomeRequireApproval, "platform"),
		}}},
		{Name: "apply", Needs: []string{"guard"}, ViewName: "v"},
	}}
	env, final, status := dagTestEnv(t, spec, map[string]error{})
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(GateSignalName("guard"), GateDecision{Approved: true, Principal: "alice"})
	}, time.Minute)
	env.ExecuteWorkflow(RunDAG, DAGInput{
		WorkflowRunID: "wr-1", WorkflowName: "guarded", Principal: "alice",
		LaunchParams: map[string]any{"environment": "prod"},
	})

	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("workflow: completed=%v err=%v", env.IsWorkflowCompleted(), env.GetWorkflowError())
	}
	want := map[string]string{"guard": stepSucceeded, "apply": stepSucceeded}
	if !reflect.DeepEqual(*final, want) {
		t.Fatalf("steps: got %v want %v", *final, want)
	}
	if *status != types.RunSucceeded {
		t.Fatalf("status: %s", *status)
	}
}

// A require_approval decision that names no approver is unsatisfiable and fails
// closed — no Gate, no silent pass (ADR-0064).
func TestRunDAG_PolicyRequiresApproval_NoApprover_FailsClosed(t *testing.T) {
	spec := types.Workflow{Name: "guarded", Steps: []types.Step{
		{Name: "guard", Policy: &types.PolicySpec{Controls: []types.Control{
			{ID: "approve", When: "ctx.environment == 'prod'", Outcome: types.OutcomeRequireApproval},
		}}},
		{Name: "apply", Needs: []string{"guard"}, ViewName: "v"},
	}}
	env, final, status := dagTestEnv(t, spec, map[string]error{})
	env.ExecuteWorkflow(RunDAG, DAGInput{
		WorkflowRunID: "wr-1", WorkflowName: "guarded", Principal: "alice",
		LaunchParams: map[string]any{"environment": "prod"},
	})

	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("workflow: completed=%v err=%v", env.IsWorkflowCompleted(), env.GetWorkflowError())
	}
	want := map[string]string{"guard": stepFailed, "apply": stepSkipped}
	if !reflect.DeepEqual(*final, want) {
		t.Fatalf("steps: got %v want %v", *final, want)
	}
	if *status != types.RunFailed {
		t.Fatalf("status: %s", *status)
	}
}

// PolicyStepStatus maps the four-way outcome to a DAG status: allow succeeds,
// everything else fails closed (ADR-0063 v1).
func TestPolicyStepStatus(t *testing.T) {
	cases := map[string]string{
		types.OutcomeAllow:           stepSucceeded,
		types.OutcomeDeny:            stepFailed,
		types.OutcomeRequireApproval: stepFailed,
		types.OutcomeEscalate:        stepFailed,
	}
	for outcome, want := range cases {
		if got := PolicyStepStatus(outcome); got != want {
			t.Fatalf("%s: got %s want %s", outcome, got, want)
		}
	}
}

// assembleChangeContext surfaces the Principal as actor and string launch params
// as labels + environment, deterministically (ADR-0063).
func TestAssembleChangeContext(t *testing.T) {
	cc := assembleChangeContext(DAGInput{
		Principal:    "alice",
		LaunchParams: map[string]any{"environment": "prod", "team": "sre", "count": 3},
	})
	if cc.Actor.ID != "alice" {
		t.Fatalf("actor: %s", cc.Actor.ID)
	}
	if cc.Environment != "prod" {
		t.Fatalf("environment: %s", cc.Environment)
	}
	if cc.Labels["team"] != "sre" {
		t.Fatalf("labels: %v", cc.Labels)
	}
	if _, ok := cc.Labels["count"]; ok {
		t.Fatalf("non-string launch params must not become labels: %v", cc.Labels)
	}
}

// Committers (SoD's source, ADR-0068) are surfaced from the `committers` launch
// param, tolerating both []string and []any.
func TestAssembleChangeContext_Committers(t *testing.T) {
	cc := assembleChangeContext(DAGInput{
		Principal:    "alice",
		LaunchParams: map[string]any{"committers": []any{"alice", "bob"}},
	})
	if len(cc.Committers) != 2 || cc.Committers[0].ID != "alice" || cc.Committers[1].ID != "bob" {
		t.Fatalf("committers not surfaced: %+v", cc.Committers)
	}
}

// An allow-outcome policy Step succeeds and downstream success-gated steps run.
func TestRunDAG_PolicyAllows(t *testing.T) {
	spec := types.Workflow{Name: "guarded", Steps: []types.Step{
		{Name: "guard", Policy: &types.PolicySpec{Controls: []types.Control{
			{ID: "prod-blast", When: "ctx.environment == 'prod' && ctx.blastRadius.entityCount > 10.0", Outcome: types.OutcomeDeny},
		}}},
		{Name: "apply", Needs: []string{"guard"}, ViewName: "v"},
	}}
	env, final, status := dagTestEnv(t, spec, map[string]error{})
	// No prod env and zero blast radius ⇒ the deny control does not fire ⇒ allow.
	env.ExecuteWorkflow(RunDAG, DAGInput{WorkflowRunID: "wr-1", WorkflowName: "guarded", Principal: "alice"})

	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("workflow: completed=%v err=%v", env.IsWorkflowCompleted(), env.GetWorkflowError())
	}
	want := map[string]string{"guard": stepSucceeded, "apply": stepSucceeded}
	if !reflect.DeepEqual(*final, want) {
		t.Fatalf("steps: got %v want %v", *final, want)
	}
	if *status != types.RunSucceeded {
		t.Fatalf("status: %s", *status)
	}
}

// A deny-outcome policy Step fails and BLOCKS the downstream (success-gated)
// step — the governance checkpoint gates the DAG (ADR-0063). The deny is driven
// by a launch-param environment surfaced into the ChangeContext.
func TestRunDAG_PolicyDenies(t *testing.T) {
	spec := types.Workflow{Name: "guarded", Steps: []types.Step{
		{Name: "guard", Policy: &types.PolicySpec{Controls: []types.Control{
			{ID: "prod-freeze", When: "ctx.environment == 'prod'", Outcome: types.OutcomeDeny},
		}}},
		{Name: "apply", Needs: []string{"guard"}, ViewName: "v"},
	}}
	env, final, status := dagTestEnv(t, spec, map[string]error{})
	env.ExecuteWorkflow(RunDAG, DAGInput{
		WorkflowRunID: "wr-1", WorkflowName: "guarded", Principal: "alice",
		LaunchParams: map[string]any{"environment": "prod"},
	})

	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("workflow: completed=%v err=%v", env.IsWorkflowCompleted(), env.GetWorkflowError())
	}
	want := map[string]string{"guard": stepFailed, "apply": stepSkipped}
	if !reflect.DeepEqual(*final, want) {
		t.Fatalf("steps: got %v want %v", *final, want)
	}
	if *status != types.RunFailed {
		t.Fatalf("denied policy must fail the workflow run, got %s", *status)
	}
}
