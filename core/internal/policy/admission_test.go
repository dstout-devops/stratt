package policy

import (
	"context"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func prodDecl() map[string]any {
	return map[string]any{
		"kind":   "Assignment",
		"spec":   map[string]any{"environment": "prod"},
		"labels": map[string]any{"team": "payments"},
	}
}

// Admission denies a declaration whose object matches a deny control (§3
// Kyverno-for-config), and admits otherwise.
func TestAdmit_DenyOnObject(t *testing.T) {
	ctrls := []types.Control{
		{ID: "no-prod-assignment", When: "object.kind == 'Assignment' && object.spec.environment == 'prod'", Outcome: types.OutcomeDeny},
	}
	if d := admit(ctrls, prodDecl()); d.Outcome != types.OutcomeDeny {
		t.Fatalf("prod Assignment must be denied, got %s (%v)", d.Outcome, d.Reasons)
	}
	dev := prodDecl()
	dev["spec"] = map[string]any{"environment": "dev"}
	if d := admit(ctrls, dev); d.Outcome != types.OutcomeAllow {
		t.Fatalf("dev Assignment must be admitted, got %s", d.Outcome)
	}
}

// An uncompilable or unevaluable admission control fails closed to deny — never
// a silent admit (§1.8).
func TestAdmit_FailsClosed(t *testing.T) {
	bad := []types.Control{{ID: "broken", When: "!!! not cel", Outcome: types.OutcomeAllow}}
	if d := admit(bad, prodDecl()); d.Outcome != types.OutcomeDeny || codes(d)["compile_error"] != 1 {
		t.Fatalf("uncompilable admission control must deny, got %s (%v)", d.Outcome, d.Reasons)
	}
	missing := []types.Control{{ID: "ref", When: "object.spec.nope == 'x'", Outcome: types.OutcomeAllow}}
	if d := admit(missing, prodDecl()); d.Outcome != types.OutcomeDeny || codes(d)["eval_error"] != 1 {
		t.Fatalf("a reference to an absent field must fail closed, got %s (%v)", d.Outcome, d.Reasons)
	}
}

// Admission runs through the same PDP port; the CEL provider evaluates, Bypass
// admits everything (recorded).
func TestAdmit_ThroughPort(t *testing.T) {
	deny := []types.Control{{ID: "d", When: "true", Outcome: types.OutcomeDeny}}
	got := CEL{}.Admit(context.Background(), AdmissionRequest{Object: prodDecl(), Controls: deny})
	if got.Outcome != types.OutcomeDeny {
		t.Fatalf("CEL provider admission must deny, got %s", got.Outcome)
	}
	byp := Bypass{}.Admit(context.Background(), AdmissionRequest{Object: prodDecl(), Controls: deny})
	if byp.Outcome != types.OutcomeAllow || codes(byp)["policy-bypassed"] != 1 || byp.Provenance.Engine != "bypass" {
		t.Fatalf("bypass must admit + record, got %s (%v)", byp.Outcome, byp.Reasons)
	}
}

func TestValidateAdmissionControls(t *testing.T) {
	if err := ValidateAdmissionControls([]types.Control{
		{ID: "ok", When: "object.spec.environment == 'prod'", Outcome: types.OutcomeDeny},
	}); err != nil {
		t.Fatalf("valid admission control must pass, got %v", err)
	}
	bad := map[string]types.Control{
		"no id":              {When: "true", Outcome: types.OutcomeDeny},
		"no when":            {ID: "x", Outcome: types.OutcomeDeny},
		"non allow/deny":     {ID: "x", When: "true", Outcome: types.OutcomeRequireApproval},
		"uncompilable":       {ID: "x", When: "!!! bad", Outcome: types.OutcomeDeny},
		"run-time primitive": {ID: "x", When: "true", Outcome: types.OutcomeDeny, TimeWindow: &types.TimeWindowSpec{Mode: types.TimeWindowDeny, EndHourUTC: 1}},
	}
	for name, c := range bad {
		if err := ValidateAdmissionControls([]types.Control{c}); err == nil {
			t.Fatalf("%s: must be rejected at load", name)
		}
	}
}
