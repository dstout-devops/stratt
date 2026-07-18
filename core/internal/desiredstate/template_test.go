package desiredstate

import (
	"context"
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
