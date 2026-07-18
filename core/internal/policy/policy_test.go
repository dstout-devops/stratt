package policy

import (
	"testing"
	"time"

	"github.com/dstout-devops/stratt/types"
)

func prodCtx() types.ChangeContext {
	return types.ChangeContext{
		Actor:       types.PrincipalRef{ID: "dev-runner", Kind: "service"},
		Environment: "prod",
		BlastRadius: types.BlastRadius{EntityCount: 50, ServiceCount: 3},
	}
}

func codes(d types.Decision) map[string]int {
	m := map[string]int{}
	for _, r := range d.Reasons {
		m[r.Code]++
	}
	return m
}

// No controls ⇒ allow (the default), with no reasons.
func TestEvaluate_NoControls_Allow(t *testing.T) {
	d := Evaluate(nil, prodCtx())
	if d.Outcome != types.OutcomeAllow {
		t.Fatalf("no controls must allow, got %s", d.Outcome)
	}
	if len(d.Reasons) != 0 {
		t.Fatalf("no controls must yield no reasons, got %v", d.Reasons)
	}
	if d.Provenance.Engine != "cel-builtin" {
		t.Fatalf("provenance engine = %q", d.Provenance.Engine)
	}
}

// A single firing deny control ⇒ deny.
func TestEvaluate_SingleDeny(t *testing.T) {
	ctrls := []types.Control{
		{ID: "no-big-prod", When: "ctx.environment == 'prod' && ctx.blastRadius.entityCount > 10.0", Outcome: types.OutcomeDeny},
	}
	d := Evaluate(ctrls, prodCtx())
	if d.Outcome != types.OutcomeDeny {
		t.Fatalf("want deny, got %s (%v)", d.Outcome, d.Reasons)
	}
	if codes(d)["fired"] != 1 {
		t.Fatalf("want one fired reason, got %v", d.Reasons)
	}
}

// The lattice is order-independent: [allow, deny] and [deny, allow] both deny,
// and BOTH fired controls are recorded (no short-circuit — ADR-0061 M3/S4).
func TestEvaluate_LatticeOrderIndependent(t *testing.T) {
	allowC := types.Control{ID: "prod-allow", When: "ctx.environment == 'prod'", Outcome: types.OutcomeAllow}
	denyC := types.Control{ID: "big-deny", When: "ctx.blastRadius.entityCount > 10.0", Outcome: types.OutcomeDeny}

	for _, order := range [][]types.Control{{allowC, denyC}, {denyC, allowC}} {
		d := Evaluate(order, prodCtx())
		if d.Outcome != types.OutcomeDeny {
			t.Fatalf("order %v: want deny, got %s", order, d.Outcome)
		}
		if got := codes(d)["fired"]; got != 2 {
			t.Fatalf("order %v: both controls must be recorded as fired, got %d reasons %v", order, got, d.Reasons)
		}
	}
}

// deny beats every lesser outcome regardless of what else fires.
func TestEvaluate_DenyDominates(t *testing.T) {
	ctrls := []types.Control{
		{ID: "a", When: "true", Outcome: types.OutcomeAllow},
		{ID: "r", When: "true", Outcome: types.OutcomeRequireApproval},
		{ID: "e", When: "true", Outcome: types.OutcomeEscalate},
		{ID: "d", When: "true", Outcome: types.OutcomeDeny},
	}
	d := Evaluate(ctrls, prodCtx())
	if d.Outcome != types.OutcomeDeny {
		t.Fatalf("want deny, got %s", d.Outcome)
	}
	if codes(d)["fired"] != 4 {
		t.Fatalf("all four controls must be recorded, got %v", d.Reasons)
	}
}

// require_approval wins over allow, and obligations are collected ONLY from the
// controls that produced the winning outcome.
func TestEvaluate_WinningObligationsOnly(t *testing.T) {
	allowC := types.Control{
		ID: "a", When: "true", Outcome: types.OutcomeAllow,
		Obligations: []types.Obligation{{Type: types.ObligationNotify, Params: map[string]any{"target": "loser"}}},
	}
	approveC := types.Control{
		ID: "r", When: "true", Outcome: types.OutcomeRequireApproval,
		Obligations: []types.Obligation{{Type: types.ObligationRequireApproval, Params: map[string]any{"count": 2}}},
	}
	d := Evaluate([]types.Control{allowC, approveC}, prodCtx())
	if d.Outcome != types.OutcomeRequireApproval {
		t.Fatalf("want require_approval, got %s", d.Outcome)
	}
	if len(d.Obligations) != 1 || d.Obligations[0].Type != types.ObligationRequireApproval {
		t.Fatalf("only the winning control's obligation must survive, got %v", d.Obligations)
	}
}

// A predicate over an ABSENT sparse risk coordinate fails CLOSED to deny —
// most-restrictive, never a silent allow (ADR-0061 M4), even when the control's
// own declared outcome is allow.
func TestEvaluate_MissingRisk_FailsClosed(t *testing.T) {
	ctrls := []types.Control{
		{ID: "risk", When: "ctx.riskScore >= 0.8", Outcome: types.OutcomeAllow},
	}
	cc := prodCtx() // RiskScore is nil
	d := Evaluate(ctrls, cc)
	if d.Outcome != types.OutcomeDeny {
		t.Fatalf("absent risk must fail closed to deny, got %s (%v)", d.Outcome, d.Reasons)
	}
	if codes(d)["eval_error"] != 1 {
		t.Fatalf("want an eval_error reason, got %v", d.Reasons)
	}
}

// A predicate that guards absence with has() evaluates safely to false and does
// not fire — so absent risk with a guarded predicate ⇒ allow.
func TestEvaluate_HasGuard_Safe(t *testing.T) {
	ctrls := []types.Control{
		{ID: "risk", When: "has(ctx.riskScore) && ctx.riskScore >= 0.8", Outcome: types.OutcomeDeny},
	}
	d := Evaluate(ctrls, prodCtx()) // RiskScore nil
	if d.Outcome != types.OutcomeAllow {
		t.Fatalf("guarded absent risk must not fire, got %s (%v)", d.Outcome, d.Reasons)
	}
}

// A guarded predicate DOES fire when the coordinate is present and matches.
func TestEvaluate_RiskPresent_Fires(t *testing.T) {
	high := 0.9
	cc := prodCtx()
	cc.RiskScore = &high
	ctrls := []types.Control{
		{ID: "risk", When: "has(ctx.riskScore) && ctx.riskScore >= 0.8", Outcome: types.OutcomeEscalate},
	}
	d := Evaluate(ctrls, cc)
	if d.Outcome != types.OutcomeEscalate {
		t.Fatalf("present high risk must escalate, got %s (%v)", d.Outcome, d.Reasons)
	}
}

// A control whose predicate will not compile fails CLOSED to deny.
func TestEvaluate_CompileError_FailsClosed(t *testing.T) {
	ctrls := []types.Control{
		{ID: "broken", When: "!!! not valid cel", Outcome: types.OutcomeAllow},
	}
	d := Evaluate(ctrls, prodCtx())
	if d.Outcome != types.OutcomeDeny {
		t.Fatalf("uncompilable control must fail closed to deny, got %s", d.Outcome)
	}
	if codes(d)["compile_error"] != 1 {
		t.Fatalf("want a compile_error reason, got %v", d.Reasons)
	}
}

// A non-bool predicate is a declaration error caught at compile ⇒ fail closed.
func TestEvaluate_NonBool_FailsClosed(t *testing.T) {
	ctrls := []types.Control{
		{ID: "notbool", When: "ctx.environment", Outcome: types.OutcomeAllow},
	}
	d := Evaluate(ctrls, prodCtx())
	if d.Outcome != types.OutcomeDeny {
		t.Fatalf("non-bool predicate must fail closed, got %s", d.Outcome)
	}
}

// ── typed Control library: TimeWindow (ADR-0067) ────────────────────────────

var (
	inHour  = time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC) // hour 10
	outHour = time.Date(2026, 7, 18, 3, 0, 0, 0, time.UTC)  // hour 3
)

func TestTimeWindow_DenyMode(t *testing.T) {
	tw := &types.TimeWindowSpec{Mode: types.TimeWindowDeny, StartHourUTC: 9, EndHourUTC: 17}
	if !timeWindowFires(tw, inHour) {
		t.Fatal("blackout must fire inside the window")
	}
	if timeWindowFires(tw, outHour) {
		t.Fatal("blackout must not fire outside the window")
	}
}

func TestTimeWindow_AllowOnly(t *testing.T) {
	tw := &types.TimeWindowSpec{Mode: types.TimeWindowAllowOnly, StartHourUTC: 9, EndHourUTC: 17}
	if timeWindowFires(tw, inHour) {
		t.Fatal("maintenance window must not fire inside the window")
	}
	if !timeWindowFires(tw, outHour) {
		t.Fatal("maintenance window must fire outside the window")
	}
}

func TestTimeWindow_DaysFilter(t *testing.T) {
	day := weekdayAbbr[inHour.Weekday()]
	other := "mon"
	if day == "mon" {
		other = "tue"
	}
	on := &types.TimeWindowSpec{Mode: types.TimeWindowDeny, Days: []string{day}, StartHourUTC: 0, EndHourUTC: 24}
	off := &types.TimeWindowSpec{Mode: types.TimeWindowDeny, Days: []string{other}, StartHourUTC: 0, EndHourUTC: 24}
	if !timeWindowFires(on, inHour) {
		t.Fatal("must fire on a matching day")
	}
	if timeWindowFires(off, inHour) {
		t.Fatal("must not fire on a non-matching day")
	}
}

// A TimeWindow control gates the decision like any other, over the fixed lattice.
func TestEvaluate_TimeWindowControl(t *testing.T) {
	freeze := types.Control{ID: "freeze", Outcome: types.OutcomeDeny,
		TimeWindow: &types.TimeWindowSpec{Mode: types.TimeWindowDeny, StartHourUTC: 9, EndHourUTC: 17}}

	ccIn := prodCtx()
	ccIn.ScheduledAt = inHour
	if d := Evaluate([]types.Control{freeze}, ccIn); d.Outcome != types.OutcomeDeny {
		t.Fatalf("in-window freeze must deny, got %s", d.Outcome)
	}
	ccOut := prodCtx()
	ccOut.ScheduledAt = outHour
	if d := Evaluate([]types.Control{freeze}, ccOut); d.Outcome != types.OutcomeAllow {
		t.Fatalf("out-of-window freeze must allow, got %s", d.Outcome)
	}
}

// A TimeWindow control with no scheduled_at fails closed (ADR-0061 M4).
func TestEvaluate_TimeWindow_NoSchedule_FailsClosed(t *testing.T) {
	freeze := types.Control{ID: "freeze", Outcome: types.OutcomeDeny,
		TimeWindow: &types.TimeWindowSpec{Mode: types.TimeWindowDeny, StartHourUTC: 9, EndHourUTC: 17}}
	d := Evaluate([]types.Control{freeze}, prodCtx()) // ScheduledAt zero
	if d.Outcome != types.OutcomeDeny {
		t.Fatalf("time-window with no scheduled_at must fail closed, got %s (%v)", d.Outcome, d.Reasons)
	}
	if codes(d)["no_schedule_time"] != 1 {
		t.Fatalf("want a no_schedule_time reason, got %v", d.Reasons)
	}
}

func TestValidateControls_TimeWindow(t *testing.T) {
	good := []types.Control{{ID: "w", Outcome: types.OutcomeDeny,
		TimeWindow: &types.TimeWindowSpec{Mode: types.TimeWindowDeny, Days: []string{"sat", "sun"}, StartHourUTC: 0, EndHourUTC: 24}}}
	if err := ValidateControls(good); err != nil {
		t.Fatalf("valid time-window must pass, got %v", err)
	}
	bad := map[string]types.Control{
		"bad mode":  {ID: "w", Outcome: types.OutcomeDeny, TimeWindow: &types.TimeWindowSpec{Mode: "sometimes", StartHourUTC: 0, EndHourUTC: 24}},
		"bad hours": {ID: "w", Outcome: types.OutcomeDeny, TimeWindow: &types.TimeWindowSpec{Mode: types.TimeWindowDeny, StartHourUTC: 17, EndHourUTC: 9}},
		"bad day":   {ID: "w", Outcome: types.OutcomeDeny, TimeWindow: &types.TimeWindowSpec{Mode: types.TimeWindowDeny, Days: []string{"funday"}, StartHourUTC: 0, EndHourUTC: 24}},
		"both kinds": {ID: "w", Outcome: types.OutcomeDeny, When: "true",
			TimeWindow: &types.TimeWindowSpec{Mode: types.TimeWindowDeny, StartHourUTC: 0, EndHourUTC: 24}},
		"no kind": {ID: "w", Outcome: types.OutcomeDeny},
	}
	for name, c := range bad {
		if err := ValidateControls([]types.Control{c}); err == nil {
			t.Fatalf("%s: must be rejected at load", name)
		}
	}
}

// ValidateControls compiles every predicate at declaration time (§1.8).
func TestValidateControls(t *testing.T) {
	ok := []types.Control{
		{ID: "a", When: "ctx.environment == 'prod'", Outcome: types.OutcomeDeny},
		{ID: "b", When: "has(ctx.riskScore) && ctx.riskScore >= 0.8", Outcome: types.OutcomeEscalate},
	}
	if err := ValidateControls(ok); err != nil {
		t.Fatalf("valid controls must pass, got %v", err)
	}
	cases := []struct {
		name string
		c    types.Control
	}{
		{"missing id", types.Control{When: "true", Outcome: types.OutcomeAllow}},
		{"unknown outcome", types.Control{ID: "x", When: "true", Outcome: "maybe"}},
		{"uncompilable predicate", types.Control{ID: "x", When: "!!! not cel", Outcome: types.OutcomeAllow}},
		{"non-bool predicate", types.Control{ID: "x", When: "ctx.environment", Outcome: types.OutcomeAllow}},
		{"empty outcome", types.Control{ID: "x", When: "true", Outcome: ""}},
	}
	for _, tc := range cases {
		if err := ValidateControls([]types.Control{tc.c}); err == nil {
			t.Fatalf("%s: must be rejected at load", tc.name)
		}
	}
}
