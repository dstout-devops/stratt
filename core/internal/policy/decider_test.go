package policy

import (
	"context"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// The built-in CEL provider decides exactly as the evaluator (ADR-0072): the
// port is a thin seam over the engine, not a second decision path.
func TestCELDecider_MatchesEvaluate(t *testing.T) {
	controls := []types.Control{
		{ID: "deny-prod", When: "ctx.environment == 'prod'", Outcome: types.OutcomeDeny},
	}
	cc := prodCtx()
	want := Evaluate(controls, cc)
	got := CEL{}.Decide(context.Background(), Request{Controls: controls, Context: cc})
	if got.Outcome != want.Outcome {
		t.Fatalf("CEL provider outcome %s != Evaluate %s", got.Outcome, want.Outcome)
	}
}

// Bypass disables governance explicitly and VISIBLY — allow, but recorded, never
// a silent skip (§1.8, ADR-0072).
func TestBypassDecider(t *testing.T) {
	deny := []types.Control{{ID: "x", When: "true", Outcome: types.OutcomeDeny}}
	d := Bypass{}.Decide(context.Background(), Request{Controls: deny, Context: prodCtx()})
	if d.Outcome != types.OutcomeAllow {
		t.Fatalf("bypass must allow, got %s", d.Outcome)
	}
	if codes(d)["policy-bypassed"] != 1 {
		t.Fatalf("bypass must record a policy-bypassed reason, got %v", d.Reasons)
	}
	if d.Provenance.Engine != "bypass" {
		t.Fatalf("bypass provenance engine = %q", d.Provenance.Engine)
	}
}

// The Decider port is satisfiable by an arbitrary provider — the swap point.
func TestDecider_IsSwappable(t *testing.T) {
	var _ Decider = CEL{}
	var _ Decider = Bypass{}
	var d Decider = Bypass{}
	if d.Decide(context.Background(), Request{}).Outcome != types.OutcomeAllow {
		t.Fatal("a swapped provider must satisfy the port")
	}
}
