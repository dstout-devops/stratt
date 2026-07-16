package desiredstate

import (
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func planStep(name string) types.Step {
	return types.Step{Name: name, ViewName: "v", Actuator: "script", Plan: true, Params: map[string]any{"script": "echo plan"}}
}
func gateFor(name, plan string, needs ...string) types.Step {
	return types.Step{Name: name, Gate: &types.GateSpec{Approvers: types.GateApprovers{Principals: []string{"alice"}}}, PlanFrom: plan, Needs: needs}
}
func applyPinned(name, plan string, needs ...string) types.Step {
	return types.Step{Name: name, ViewName: "v", Actuator: "script", PlanFrom: plan, Needs: needs, Params: map[string]any{"script": "echo apply"}}
}

// TestPlanPinning_ValidTriple: plan -> gate(binds plan, needs plan) -> apply(pins
// plan, needs gate) is accepted.
func TestPlanPinning_ValidTriple(t *testing.T) {
	wf := types.Workflow{Name: "w", Steps: []types.Step{
		planStep("plan"), gateFor("gate", "plan", "plan"), applyPinned("apply", "plan", "gate"),
	}}
	if err := ValidateWorkflow(wf); err != nil {
		t.Fatalf("a well-formed Plan<->Gate<->Apply triple must validate: %v", err)
	}
}

// TestPlanPinning_UnguardedApplyRejected: the most dangerous hole — a plan-pinned
// Apply with NO guarding Gate must be a compile error (never a silent unpinned
// live apply of `desired`).
func TestPlanPinning_UnguardedApplyRejected(t *testing.T) {
	wf := types.Workflow{Name: "w", Steps: []types.Step{
		planStep("plan"), applyPinned("apply", "plan", "plan"), // needs plan directly, no gate
	}}
	err := ValidateWorkflow(wf)
	if err == nil || !strings.Contains(err.Error(), "not guarded by a Gate") {
		t.Fatalf("plan-pinned Apply with no guarding Gate must be rejected (fail-closed), got %v", err)
	}
}

// TestPlanPinning_UnknownPlanFromRejected: planFrom must name an existing step.
func TestPlanPinning_UnknownPlanFromRejected(t *testing.T) {
	wf := types.Workflow{Name: "w", Steps: []types.Step{
		gateFor("gate", "ghost", "gate"), // needs itself will fail earlier; use a real cycle-free shape
	}}
	// Rebuild without the self-need to isolate the planFrom check.
	wf.Steps = []types.Step{applyPinned("apply", "ghost")}
	err := ValidateWorkflow(wf)
	if err == nil || !strings.Contains(err.Error(), "unknown step") {
		t.Fatalf("planFrom naming an unknown step must be rejected, got %v", err)
	}
}

// TestPlanPinning_NonPlanSourceRejected: planFrom must name a PLAN step.
func TestPlanPinning_NonPlanSourceRejected(t *testing.T) {
	notPlan := types.Step{Name: "prep", ViewName: "v", Actuator: "script", Params: map[string]any{"script": "echo x"}}
	wf := types.Workflow{Name: "w", Steps: []types.Step{
		notPlan, gateFor("gate", "prep", "prep"), applyPinned("apply", "prep", "gate"),
	}}
	err := ValidateWorkflow(wf)
	if err == nil || !strings.Contains(err.Error(), "not a plan step") {
		t.Fatalf("planFrom naming a non-plan step must be rejected, got %v", err)
	}
}

// TestPlanPinning_GateMustNeedItsPlan: a Gate that binds a plan it does not `needs`
// (so the digest would not exist to bind) is rejected.
func TestPlanPinning_GateMustNeedItsPlan(t *testing.T) {
	wf := types.Workflow{Name: "w", Steps: []types.Step{
		planStep("plan"), gateFor("gate", "plan"), applyPinned("apply", "plan", "gate"),
	}}
	err := ValidateWorkflow(wf)
	if err == nil || !strings.Contains(err.Error(), "does not need it") {
		t.Fatalf("a Gate binding a plan it does not need must be rejected, got %v", err)
	}
}
