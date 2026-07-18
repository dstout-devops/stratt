package orchestrate

import (
	"reflect"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

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
