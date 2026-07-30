package desiredstate

import (
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// TestStepDeclaringNoShapeIsRefused closes the one input on which the load-time step-shape
// check and the runtime one DISAGREED.
//
// ValidateWorkflow classifies positively — isGate/isPolicy/isAction/isActuation/isNested,
// each from the fields that shape actually uses. types.Step.IsActuation() classifies
// RESIDUALLY — "not a gate, not a policy, not nested, not an action" — so the DAG and the
// §2.5 authz door fail CLOSED on a Step neither can name. Both are right: an actuation Step
// may legitimately omit `viewName` (it inherits the Assignment's View), and treating an
// unclassifiable Step as needing authorization is the safe default.
//
// They met on a Step that declares nothing:
//
//	steps:
//	  - name: converge
//
// Positively it is no shape, and no case refused it. Residually it is an actuation — so it
// loaded clean, dispatched, and reached the actuation path with an empty Actuator.
//
// The fix is at LOAD, not at dispatch, on purpose: fail-closed is the correct runtime
// posture, and once nothing shapeless can load, the two definitions agree on every Step that
// exists.
func TestStepDeclaringNoShapeIsRefused(t *testing.T) {
	w := types.Workflow{Name: "wf", Steps: []types.Step{{Name: "converge"}}}

	// The runtime predicate's verdict on this Step is what makes it dangerous — assert it,
	// so a future change to IsActuation() that removes the hazard also fails this test and
	// invites the load check to be revisited rather than left as dead weight.
	if !w.Steps[0].IsActuation() {
		t.Fatal("types.Step.IsActuation() no longer calls a shapeless Step an actuation — the " +
			"runtime posture changed, so revisit whether this load-time refusal is still the fix")
	}

	err := ValidateWorkflow(w)
	if err == nil {
		t.Fatal("a Step declaring no shape must be refused at load — it would dispatch as an " +
			"actuation against an empty Actuator")
	}
	if !strings.Contains(err.Error(), "declares no shape") {
		t.Fatalf("the error must say what is wrong with the STEP, not fail somewhere downstream; got: %v", err)
	}
	if !strings.Contains(err.Error(), "converge") {
		t.Fatalf("the error must name the step (§1.8); got: %v", err)
	}
}

// Every legitimate shape still loads. Without this, "declares no shape" is one over-eager
// condition away from refusing a Workflow the estate actually ships — and the four shapes
// are not symmetrical (an actuation may omit viewName, an action may name a capability
// instead of an Action).
func TestEveryDeclaredStepShapeStillLoads(t *testing.T) {
	cases := []struct {
		name string
		step types.Step
	}{
		{"gate", types.Step{Name: "approve", Gate: &types.GateSpec{
			Approvers: types.GateApprovers{Principals: []string{"alice"}}}}},
		{"policy", types.Step{Name: "check", Policy: &types.PolicySpec{
			Controls: []types.Control{{ID: "c", When: "true", Outcome: types.OutcomeAllow}}}}},
		{"nested workflow", types.Step{Name: "child", Workflow: "other-wf"}},
		{"actuation inheriting its view", types.Step{Name: "converge", Actuator: "declared"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := ValidateWorkflow(types.Workflow{Name: "wf", Steps: []types.Step{c.step}}); err != nil {
				if strings.Contains(err.Error(), "declares no shape") {
					t.Fatalf("a legitimate %s step was refused as shapeless: %v", c.name, err)
				}
				// Other errors (an unknown actuator's Contract, say) are another check's business.
				t.Logf("loaded with an unrelated error, which is fine here: %v", err)
			}
		})
	}
}
