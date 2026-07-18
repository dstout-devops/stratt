package desiredstate

import (
	"context"
	"os"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func TestViewSelectorNamespaceScope(t *testing.T) {
	root := t.TempDir()
	// A param-bound View is fine.
	writeDecl(t, root, "ok.yaml", "name: ok\nselector: {kinds: [vm], labels: {host: \"{{.param.host}}\"}}\n")
	if _, err := ParseDir(root); err != nil {
		t.Fatalf("param View must be allowed: %v", err)
	}
	// An event/spec binding in a View selector is rejected at declaration.
	for _, bad := range []string{
		"name: b\nselector: {kinds: [vm], labels: {host: \"{{.event.host}}\"}}\n",
		"name: b\nselector: {kinds: [vm], labels: {host: \"{{.spec.host}}\"}}\n",
	} {
		d := t.TempDir()
		writeDecl(t, d, "b.yaml", bad)
		if _, err := ParseDir(d); err == nil {
			t.Fatalf("View selector with a non-param namespace must be rejected: %s", bad)
		}
	}
}

func TestParametrizedViewPlanOmitsMemberCount(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	decls := Declarations{Views: []Declaration{{
		Name:     "param-vms",
		Selector: types.ViewSelector{Kinds: []string{"vm"}, Labels: map[string]string{"host": "{{.param.host}}"}},
	}}}
	plan, err := ComputePlan(ctx, s, decls)
	if err != nil {
		t.Fatal(err)
	}
	var e *PlanEntry
	for i := range plan.Entries {
		if plan.Entries[i].Name == "param-vms" {
			e = &plan.Entries[i]
		}
	}
	if e == nil || !e.ParamDependent || e.MemberCount != 0 {
		t.Fatalf("parametrized View must be param-dependent with no member count: %+v", e)
	}
}

func TestWorkflowStepParamNamespaceScope(t *testing.T) {
	// A Step param may bind the event namespace (from the firing event).
	ok := types.Workflow{Name: "w", Steps: []types.Step{
		{Name: "s", ViewName: "v", Actuator: "script", Params: map[string]any{"script": "echo {{.event.host}}"}},
	}}
	if err := ValidateWorkflow(ok); err != nil {
		t.Fatalf("event binding on a Step must be allowed: %v", err)
	}
	// A Step param may bind the launch namespace (operator-supplied launch params
	// for a parameterized build/re-placement Workflow, ADR-0059).
	okLaunch := types.Workflow{Name: "w", Steps: []types.Step{
		{Name: "s", ViewName: "v", Actuator: "script", Params: map[string]any{"script": "echo {{.launch.targetSubnet}}"}},
	}}
	if err := ValidateWorkflow(okLaunch); err != nil {
		t.Fatalf("launch binding on a Step must be allowed: %v", err)
	}
	// spec/param are not available on a Step.
	bad := types.Workflow{Name: "w", Steps: []types.Step{
		{Name: "s", ViewName: "v", Actuator: "script", Params: map[string]any{"script": "{{.spec.x}}"}},
	}}
	if err := ValidateWorkflow(bad); err == nil {
		t.Fatal("spec namespace on a Step must be rejected")
	}
}

// A policy Step is a valid Step shape; its control predicates are CEL-compiled
// at load and a bad predicate or empty control set fails the file (ADR-0063).
func TestWorkflowPolicyStep(t *testing.T) {
	good := types.Workflow{Name: "w", Steps: []types.Step{
		{Name: "guard", Policy: &types.PolicySpec{Controls: []types.Control{
			{ID: "freeze", When: "ctx.environment == 'prod'", Outcome: types.OutcomeDeny},
		}}},
	}}
	if err := ValidateWorkflow(good); err != nil {
		t.Fatalf("valid policy step must pass, got %v", err)
	}
	bads := map[string]types.Workflow{
		"empty controls": {Name: "w", Steps: []types.Step{
			{Name: "guard", Policy: &types.PolicySpec{}},
		}},
		"uncompilable predicate": {Name: "w", Steps: []types.Step{
			{Name: "guard", Policy: &types.PolicySpec{Controls: []types.Control{
				{ID: "x", When: "!!! not cel", Outcome: types.OutcomeDeny}},
			}},
		}},
		"mixed shape (policy+actuation)": {Name: "w", Steps: []types.Step{
			{Name: "guard", ViewName: "v", Policy: &types.PolicySpec{Controls: []types.Control{
				{ID: "x", When: "true", Outcome: types.OutcomeDeny}},
			}},
		}},
	}
	for name, wf := range bads {
		if err := ValidateWorkflow(wf); err == nil {
			t.Fatalf("%s: must be rejected at load", name)
		}
	}
}

// M1 (ADR-0061 / ADR-0066): the §5 plan-gate floor is non-substitutable — a
// plan-pinned Apply "guarded" only by a policy Step (which binds no plan digest)
// is rejected at load, exactly as one guarded by nothing. A real plan-binding
// Gate is required.
func TestPlanGateFloorNotSubstitutableByPolicy(t *testing.T) {
	act := func(name string, needs []string) types.Step {
		return types.Step{Name: name, Needs: needs, ViewName: "v", Actuator: "script",
			Params: map[string]any{"script": "echo hi"}}
	}
	planStep := act("plan", nil)
	planStep.Plan = true

	applyPolicy := act("apply", []string{"guard"})
	applyPolicy.PlanFrom = "plan"
	policyGuarded := types.Workflow{Name: "w", Steps: []types.Step{
		planStep,
		{Name: "guard", Needs: []string{"plan"}, Policy: &types.PolicySpec{Controls: []types.Control{
			{ID: "c", When: "true", Outcome: types.OutcomeRequireApproval}},
		}},
		applyPolicy,
	}}
	if err := ValidateWorkflow(policyGuarded); err == nil {
		t.Fatal("a plan-pinned Apply guarded only by a policy Step must be rejected (the plan-gate floor is Gate-only, §5)")
	}
	// The same shape with a real plan-binding Gate passes.
	applyGate := act("apply", []string{"approve"})
	applyGate.PlanFrom = "plan"
	gateGuarded := types.Workflow{Name: "w", Steps: []types.Step{
		planStep,
		{Name: "approve", Needs: []string{"plan"}, PlanFrom: "plan", Gate: &types.GateSpec{
			Approvers: types.GateApprovers{Teams: []string{"platform"}}}},
		applyGate,
	}}
	if err := ValidateWorkflow(gateGuarded); err != nil {
		t.Fatalf("a plan-pinned Apply behind a real plan-binding Gate must pass, got %v", err)
	}
}

// A policy Step + its typed control library round-trips from estate YAML through
// the loader into a typed PolicySpec (ADR-0063/0067–0071 declaration surface).
func TestParseWorkflowPolicyStep(t *testing.T) {
	src := `name: guarded-deploy
steps:
  - name: guard
    policy:
      controls:
        - id: prod-freeze
          timeWindow: { mode: deny, days: [sat, sun], startHourUtc: 0, endHourUtc: 24 }
          outcome: deny
        - id: four-eyes
          sod: { distinctFrom: [committers] }
          outcome: require_approval
          obligations:
            - type: require_approval
              params: { teams: [platform-admins], count: 2 }
        - id: waive-freeze
          waiver:
            controlRef: prod-freeze
            expiresAt: 2026-07-20T00:00:00Z
            justification: incident-4471
            approvedBy: sre-lead
  - name: apply
    needs: [guard]
    viewName: v
    actuator: script
    params: { script: "echo hi" }
`
	_, wf, err := parseWorkflowFile("guarded.yaml", []byte(src))
	if err != nil {
		t.Fatalf("valid policy workflow must parse + validate, got %v", err)
	}
	if wf.Steps[0].Policy == nil || len(wf.Steps[0].Policy.Controls) != 3 {
		t.Fatalf("policy step did not round-trip: %+v", wf.Steps[0].Policy)
	}
	cs := wf.Steps[0].Policy.Controls
	if cs[0].TimeWindow == nil || cs[0].TimeWindow.Mode != types.TimeWindowDeny || len(cs[0].TimeWindow.Days) != 2 {
		t.Fatalf("time-window control mismapped: %+v", cs[0])
	}
	if cs[1].SoD == nil || cs[1].SoD.DistinctFrom[0] != types.SoDDistinctFromCommitters || cs[1].Outcome != types.OutcomeRequireApproval {
		t.Fatalf("sod control mismapped: %+v", cs[1])
	}
	if len(cs[1].Obligations) != 1 || cs[1].Obligations[0].Type != types.ObligationRequireApproval {
		t.Fatalf("obligation mismapped: %+v", cs[1].Obligations)
	}
	if cs[2].Waiver == nil || cs[2].Waiver.ControlRef != "prod-freeze" || cs[2].Waiver.ApprovedBy != "sre-lead" {
		t.Fatalf("waiver control mismapped: %+v", cs[2])
	}
	if y := cs[2].Waiver.ExpiresAt.UTC().Year(); y != 2026 {
		t.Fatalf("waiver expiresAt not parsed as a timestamp: %v", cs[2].Waiver.ExpiresAt)
	}
}

// The shipped estate governance example must boot-validate — CI guards it so the
// declaration surface and the example never drift apart.
func TestEstateChangeReviewValidates(t *testing.T) {
	raw, err := os.ReadFile("../../../estate/workflows/change-review.yaml")
	if err != nil {
		t.Fatalf("read estate example: %v", err)
	}
	if _, _, err := parseWorkflowFile("change-review.yaml", raw); err != nil {
		t.Fatalf("estate governance example must parse + validate at load: %v", err)
	}
}

// An invalid control (uncompilable predicate) fails the file at load.
func TestParseWorkflowPolicyStep_InvalidRejected(t *testing.T) {
	src := `name: bad
steps:
  - name: guard
    policy:
      controls:
        - id: broken
          when: "!!! not cel"
          outcome: deny
`
	if _, _, err := parseWorkflowFile("bad.yaml", []byte(src)); err == nil {
		t.Fatal("an uncompilable control must fail the file at load")
	}
}

func TestTriggerTemplateNamespaceScope(t *testing.T) {
	// event binding on an event-kind Trigger is allowed.
	ev := types.Trigger{
		Name: "ok", Kind: types.TriggerEvent, Emitter: "alerts", When: "true",
		ViewName: "v", Actuator: "ansible", ViewParams: map[string]any{"host": "{{.event.labels.instance}}"},
	}
	if err := ValidateTrigger(ev); err != nil {
		t.Fatalf("event binding on event Trigger must be allowed: %v", err)
	}

	// event binding on a SCHEDULE Trigger is rejected (no firing event).
	sched := types.Trigger{
		Name: "bad", Kind: types.TriggerSchedule, Cron: "@hourly",
		ViewName: "v", ViewParams: map[string]any{"host": "{{.event.x}}"},
	}
	if err := ValidateTrigger(sched); err == nil {
		t.Fatal("event template on a schedule Trigger must be rejected")
	}

	// spec/param namespaces are never available on a Trigger.
	spec := types.Trigger{
		Name: "bad2", Kind: types.TriggerEvent, Emitter: "alerts", When: "true",
		ViewName: "v", Params: map[string]any{"script": "{{.spec.package}}"},
	}
	if err := ValidateTrigger(spec); err == nil {
		t.Fatal("spec namespace must be rejected on a Trigger")
	}
}

func TestTriggerTemplatedParamsSkipPlanValidation(t *testing.T) {
	// A templated param on a typed field passes plan-time (it validates at
	// launch against the resolved value) — no contract error here.
	tr := types.Trigger{
		Name: "t", Kind: types.TriggerEvent, Emitter: "alerts", When: "true",
		ViewName: "v", Actuator: "opentofu",
		Params: map[string]any{"module": "{{.event.mod}}", "mode": "plan", "workspace": "{{.event.ws}}"},
	}
	if err := ValidateTrigger(tr); err != nil {
		t.Fatalf("templated params must defer contract validation to launch: %v", err)
	}
}
