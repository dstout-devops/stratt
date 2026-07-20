// Package policy is the built-in Policy Decision Point (ADR-0061 / ADR-0062):
// it evaluates an ordered set of governance Controls over a shared, typed
// ChangeContext and returns the four-way Decision. It is the content-blind
// built-in tier — CEL predicates over the typed Envelope only, never the
// opaque tool Payload (ADR-0046). External engines (OPA/Cerbos/Cedar) are
// plugins that normalise to the same Decision shape (ADR-0061 §7.5).
//
// This slice is the pure evaluator: it is not yet wired into the DAG, does not
// compose with OpenFGA grants, and does not compile the mandatory §4.3/§5
// floors — those are the ADR-0061 §7.2 follow-up. Here we prove the decision
// engine in isolation: all-controls-evaluated, the fixed most-restrictive
// lattice, and fail-safe/fail-closed handling.
package policy

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/cel-go/cel"

	"github.com/dstout-devops/stratt/core/internal/rules"
	"github.com/dstout-devops/stratt/types"
)

// engineName identifies the built-in evaluator in Decision provenance.
const engineName = "cel-builtin"

// policyEnv builds the CEL environment Control predicates evaluate against. It
// binds a single typed variable, `ctx` — the ChangeContext — so a predicate
// reads `ctx.environment == "prod"`, `ctx.blastRadius.entityCount > 20`, or
// `has(ctx.riskScore) && ctx.riskScore >= 0.8`. This is a deliberately tighter
// surface than the trigger env (which also binds a dyn `event`): Control
// authors are less trusted than platform-only trigger authors (dependency-
// scout, ADR-0061 §7.1). Deeper builtin/macro subsetting is a tracked
// hardening; v1 constrains the variable surface.
func policyEnv() (*cel.Env, error) {
	return cel.NewEnv(cel.Variable("ctx", cel.DynType))
}

// Evaluate runs every Control's predicate over the ChangeContext and combines
// the fired outcomes by the fixed, non-configurable most-restrictive-wins
// lattice (deny > escalate > require_approval > allow — ADR-0061 M3). Key
// invariants:
//   - ALL controls are always evaluated; order is non-semantic.
//   - EVERY fired control contributes a Reason — not only the winner (§1.8, S4).
//   - A control that will not compile, or whose predicate errors (e.g. a
//     reference to an absent sparse coordinate like ctx.riskScore, ADR-0061 M4),
//     FAILS CLOSED to deny with a reason — never a silent allow.
//   - With no control firing, the outcome is allow.
func Evaluate(controls []types.Control, cc types.ChangeContext) types.Decision {
	dec := types.Decision{
		Outcome: types.OutcomeAllow,
		Provenance: types.DecisionProvenance{
			Engine:      engineName,
			EvaluatedAt: time.Now().UTC(),
		},
	}

	env, err := policyEnv()
	if err != nil {
		// A broken environment is a programmer error, not a policy result;
		// fail closed rather than silently permit.
		dec.Outcome = types.OutcomeDeny
		dec.Reasons = []types.Reason{{Code: "env_error", Message: err.Error()}}
		return dec
	}

	cm := ctxMap(cc)
	// Pass 1: the set of control IDs exempted by an ACTIVE waiver (ADR-0069). A
	// waiver is active only if it has not expired at the decision time; an
	// unjudgeable time leaves it inactive (fail-safe — the underlying control
	// stands). Waivers reference only ControlSet controls, so they can never
	// exempt a mandatory floor (ADR-0066).
	waived := map[string]bool{}
	bgBy := map[string]string{} // bypassed control ID → the active break-glass's ID
	for _, c := range controls {
		if c.Waiver != nil && waiverActive(c.Waiver, cc.ScheduledAt) {
			waived[c.Waiver.ControlRef] = true
		}
		if c.BreakGlass != nil && breakGlassActive(c.BreakGlass, cc) {
			for _, ref := range c.BreakGlass.Bypasses {
				bgBy[ref] = c.ID
			}
		}
	}
	// Pass 2: evaluate predicate controls; an active waiver or break-glass
	// suppresses a fired control's outcome (recorded, not applied).
	bgUsed := map[string]bool{}
	var fired []types.Control
	for _, c := range controls {
		if c.Waiver != nil || c.BreakGlass != nil {
			continue // modifiers do not fire
		}
		ok, failCode, failMsg := controlFires(env, cm, cc, c)
		if failCode != "" {
			// Fail-closed: a control that cannot be evaluated (uncompilable CEL,
			// a reference to an absent coordinate, an unjudgeable window) denies —
			// most-restrictive, never a silent allow (ADR-0061 M4). A broken
			// control's deny is NOT waivable/bypassable (a modifier exempts a
			// decision, not an evaluation failure).
			dec.Outcome = mostRestrictive(dec.Outcome, types.OutcomeDeny)
			dec.Reasons = append(dec.Reasons, types.Reason{
				Code: failCode, Message: failMsg, ControlID: c.ID,
			})
			continue
		}
		if !ok {
			continue // predicate false: this control does not fire
		}
		if waived[c.ID] {
			// An active, approved waiver exempts this fired control — the outcome
			// is NOT applied, but the exemption is recorded (compliance-relevant,
			// ADR-0061 S1).
			dec.Reasons = append(dec.Reasons, types.Reason{
				Code: "waived", Message: firedMessage(c) + " (waived)", ControlID: c.ID,
			})
			continue
		}
		if bg, ok := bgBy[c.ID]; ok {
			// An active break-glass bypasses this fired control (ADR-0070). Mark
			// it used so the mandatory post-review obligation is emitted below.
			bgUsed[bg] = true
			dec.Reasons = append(dec.Reasons, types.Reason{
				Code: "break-glass", Message: firedMessage(c) + " (break-glass bypass)", ControlID: c.ID,
			})
			continue
		}
		fired = append(fired, c)
		dec.Outcome = mostRestrictive(dec.Outcome, c.Outcome)
		dec.Reasons = append(dec.Reasons, types.Reason{
			Code: "fired", Message: firedMessage(c), ControlID: c.ID,
		})
	}
	// Break-glass that actually bypassed a control leaves a MANDATORY post-review
	// obligation — bypass is never silence (ADR-0061 guardrail 6 / ADR-0070).
	for _, c := range controls {
		if c.BreakGlass != nil && bgUsed[c.ID] {
			dec.Obligations = append(dec.Obligations, types.Obligation{
				Type:   types.ObligationPostReview,
				Params: map[string]any{"by": c.BreakGlass.PostReviewBy, "incident": cc.Labels["incident"]},
			})
			dec.Reasons = append(dec.Reasons, types.Reason{
				Code: "break-glass-used", Message: "emergency bypass — mandatory post-review", ControlID: c.ID,
			})
		}
	}

	// Obligations are the binding riders of the controls that produced the
	// winning outcome; a control overridden by a more-restrictive outcome
	// carries no binding obligation into this decision.
	for _, c := range fired {
		if c.Outcome == dec.Outcome {
			dec.Obligations = append(dec.Obligations, c.Obligations...)
		}
	}
	return dec
}

// controlFires evaluates one control's predicate. It returns fired=true when the
// control applies. failCode!="" means the control could not be judged and the
// caller fails closed (deny). Dispatch is by KIND: a typed primitive is
// evaluated by dedicated deterministic logic; otherwise the raw CEL `When`
// predicate is compiled and run (ADR-0067).
func controlFires(env *cel.Env, cm map[string]any, cc types.ChangeContext, c types.Control) (fired bool, failCode, failMsg string) {
	switch {
	case c.TimeWindow != nil:
		if cc.ScheduledAt.IsZero() {
			return false, "no_schedule_time", "time-window control has no scheduled_at to judge (fail-closed, M4)"
		}
		return timeWindowFires(c.TimeWindow, cc.ScheduledAt), "", ""
	case c.SoD != nil:
		return sodViolated(c.SoD, cc), "", ""
	default: // raw CEL predicate
		prg, err := rules.CompileForEnv(env, c.When)
		if err != nil {
			return false, "compile_error", err.Error()
		}
		ok, err := prg.EvalVars(map[string]any{"ctx": cm})
		if err != nil {
			return false, "eval_error", err.Error()
		}
		return ok, "", ""
	}
}

// timeWindowFires reports whether a TimeWindow control applies at t (ADR-0067):
// a deny/blackout fires when t is INSIDE the window, an allow-only/maintenance
// window fires when t is OUTSIDE it.
func timeWindowFires(tw *types.TimeWindowSpec, t time.Time) bool {
	in := inWindow(tw, t.UTC())
	if tw.Mode == types.TimeWindowAllowOnly {
		return !in
	}
	return in // TimeWindowDeny (default)
}

var weekdayAbbr = map[time.Weekday]string{
	time.Sunday: "sun", time.Monday: "mon", time.Tuesday: "tue", time.Wednesday: "wed",
	time.Thursday: "thu", time.Friday: "fri", time.Saturday: "sat",
}

// inWindow reports whether t (UTC) falls in the recurring weekly window: a
// matching day (empty Days = every day) AND hour in [StartHourUTC, EndHourUTC).
func inWindow(tw *types.TimeWindowSpec, t time.Time) bool {
	if len(tw.Days) > 0 {
		day := weekdayAbbr[t.Weekday()]
		match := false
		for _, d := range tw.Days {
			if d == day {
				match = true
				break
			}
		}
		if !match {
			return false
		}
	}
	h := t.Hour()
	return h >= tw.StartHourUTC && h < tw.EndHourUTC
}

// sodViolated reports whether a separation-of-duties control is violated
// (ADR-0068): the actor belongs to a role set it must be distinct from. v1
// checks `committers` — the actor is also a change author. No committers ⇒ no
// dual-role conflict ⇒ not violated (plain set membership).
func sodViolated(sod *types.SoDSpec, cc types.ChangeContext) bool {
	for _, from := range sod.DistinctFrom {
		if from != types.SoDDistinctFromCommitters {
			continue
		}
		for _, c := range cc.Committers {
			if c.ID != "" && c.ID == cc.Actor.ID {
				return true
			}
		}
	}
	return false
}

// waiverActive reports whether a waiver still exempts at the decision time
// (ADR-0069): not expired. An unjudgeable time (zero scheduled_at) leaves it
// INACTIVE — we cannot confirm it has not expired, so the underlying control
// stands (fail-safe, ADR-0061 M4).
func waiverActive(w *types.WaiverSpec, at time.Time) bool {
	if at.IsZero() {
		return false
	}
	return at.Before(w.ExpiresAt)
}

// breakGlassActive reports whether a real emergency is declared (ADR-0070):
// change_class == "emergency" AND an incident and reasonCode are present (the
// activator's emergency justification, supplied at launch). Break-glass is a
// declared emergency path, never a bare flag — so it cannot be "always on".
func breakGlassActive(_ *types.BreakGlassSpec, cc types.ChangeContext) bool {
	return cc.ChangeClass == "emergency" &&
		cc.Labels["incident"] != "" &&
		cc.Labels["reasonCode"] != ""
}

// mostRestrictive returns whichever of two outcomes ranks higher on the fixed
// lattice. It is commutative and associative, so evaluation order cannot change
// the result (the §2.4 additive-union analogue, not precedence).
func mostRestrictive(a, b string) string {
	if types.OutcomeRank(b) > types.OutcomeRank(a) {
		return b
	}
	return a
}

// ctxMap renders the ChangeContext as the CEL `ctx` binding. An absent optional
// coordinate (nil RiskScore, empty Criticality) is simply missing from the map,
// so a predicate that references it errors and fails closed (ADR-0061 M4)
// unless it guards with has().
func ctxMap(cc types.ChangeContext) map[string]any {
	b, _ := json.Marshal(cc) // this struct never fails to marshal
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// ValidateControls compiles every control's When predicate against the policy
// env, returning the first error — the declaration-time gate (§1.8: fail the
// file at load, never silently at decision time). It mirrors the trigger-rule
// compile and validatePlanPinning. It also requires a non-empty, recognised
// Outcome so a control cannot declare an unknown verdict.
func ValidateControls(controls []types.Control) error {
	env, err := policyEnv()
	if err != nil {
		return err
	}
	ids := map[string]bool{}
	for _, c := range controls {
		if c.ID != "" {
			ids[c.ID] = true
		}
	}
	for _, c := range controls {
		if c.ID == "" {
			return fmt.Errorf("policy: control requires an id")
		}
		// Modifiers (waiver, break-glass) are not predicates (ADR-0069/0070): they
		// need no outcome, are exclusive with the predicate kinds and each other,
		// and reference controls in this set.
		if c.Waiver != nil || c.BreakGlass != nil {
			if c.When != "" || c.TimeWindow != nil || c.SoD != nil {
				return fmt.Errorf("policy: control %q: a modifier (waiver/break-glass) is not also a predicate", c.ID)
			}
			if c.Waiver != nil && c.BreakGlass != nil {
				return fmt.Errorf("policy: control %q: a control is a waiver or a break-glass, not both", c.ID)
			}
			if c.Waiver != nil {
				if err := validateWaiver(c.ID, c.Waiver, ids); err != nil {
					return err
				}
			} else {
				if err := validateBreakGlass(c.ID, c.BreakGlass, ids); err != nil {
					return err
				}
			}
			continue
		}
		switch c.Outcome {
		case types.OutcomeAllow, types.OutcomeDeny, types.OutcomeRequireApproval, types.OutcomeEscalate:
		default:
			return fmt.Errorf("policy: control %q: unknown outcome %q", c.ID, c.Outcome)
		}
		// A predicate control is exactly ONE kind: a raw CEL predicate or one
		// typed primitive (ADR-0067). Count the kinds and reject zero or multiple.
		kinds := 0
		if c.When != "" {
			kinds++
		}
		if c.TimeWindow != nil {
			kinds++
			if err := validateTimeWindow(c.ID, c.TimeWindow); err != nil {
				return err
			}
		}
		if c.SoD != nil {
			kinds++
			if err := validateSoD(c.ID, c.SoD); err != nil {
				return err
			}
		}
		switch kinds {
		case 0:
			return fmt.Errorf("policy: control %q: must be a CEL `when` or a typed primitive", c.ID)
		case 1:
		default:
			return fmt.Errorf("policy: control %q: is more than one kind (a CEL `when` and a typed primitive)", c.ID)
		}
		if c.When != "" {
			if _, err := rules.CompileForEnv(env, c.When); err != nil {
				return fmt.Errorf("policy: control %q: %w", c.ID, err)
			}
		}
	}
	return nil
}

// validateWaiver checks a waiver at load (ADR-0069): a mandatory expiresAt
// (guardrail 4), a justification and approver (an exemption must be accountable),
// and a controlRef naming another control IN this set — so a waiver can never
// exempt a mandatory floor (ADR-0066), only a peer ControlSet control.
func validateWaiver(id string, w *types.WaiverSpec, ids map[string]bool) error {
	if w.ControlRef == "" {
		return fmt.Errorf("policy: waiver %q requires controlRef", id)
	}
	if w.ControlRef == id {
		return fmt.Errorf("policy: waiver %q cannot reference itself", id)
	}
	if !ids[w.ControlRef] {
		return fmt.Errorf("policy: waiver %q references control %q not in this set", id, w.ControlRef)
	}
	if w.ExpiresAt.IsZero() {
		return fmt.Errorf("policy: waiver %q requires expiresAt (guardrail 4: no unbounded exemptions)", id)
	}
	if w.Justification == "" {
		return fmt.Errorf("policy: waiver %q requires a justification", id)
	}
	if w.ApprovedBy == "" {
		return fmt.Errorf("policy: waiver %q requires approvedBy", id)
	}
	return nil
}

// validateBreakGlass checks a break-glass at load (ADR-0070): a mandatory
// post-review authority (guardrail 6: bypass is never silence), and a non-empty
// Bypasses of non-self control IDs present IN this set — so it can never bypass
// a mandatory floor (ADR-0066), only peer ControlSet controls.
func validateBreakGlass(id string, bg *types.BreakGlassSpec, ids map[string]bool) error {
	if bg.PostReviewBy == "" {
		return fmt.Errorf("policy: break-glass %q requires postReviewBy (mandatory post-review, guardrail 6)", id)
	}
	if len(bg.Bypasses) == 0 {
		return fmt.Errorf("policy: break-glass %q requires bypasses", id)
	}
	for _, ref := range bg.Bypasses {
		if ref == id {
			return fmt.Errorf("policy: break-glass %q cannot bypass itself", id)
		}
		if !ids[ref] {
			return fmt.Errorf("policy: break-glass %q bypasses control %q not in this set", id, ref)
		}
	}
	return nil
}

// validateSoD checks a SoD spec at load (ADR-0068): a non-empty distinctFrom of
// recognised role sets (v1: committers).
func validateSoD(id string, sod *types.SoDSpec) error {
	if len(sod.DistinctFrom) == 0 {
		return fmt.Errorf("policy: control %q: sod requires distinctFrom", id)
	}
	for _, from := range sod.DistinctFrom {
		if from != types.SoDDistinctFromCommitters {
			return fmt.Errorf("policy: control %q: sod distinctFrom %q unsupported (v1: %q)", id, from, types.SoDDistinctFromCommitters)
		}
	}
	return nil
}

// validateTimeWindow checks a TimeWindow spec at load (ADR-0067): a known mode,
// a well-ordered hour range within [0,24], and recognised day abbreviations.
func validateTimeWindow(id string, tw *types.TimeWindowSpec) error {
	switch tw.Mode {
	case types.TimeWindowDeny, types.TimeWindowAllowOnly:
	default:
		return fmt.Errorf("policy: control %q: time-window mode %q must be %q or %q", id, tw.Mode, types.TimeWindowDeny, types.TimeWindowAllowOnly)
	}
	if tw.StartHourUTC < 0 || tw.EndHourUTC > 24 || tw.StartHourUTC >= tw.EndHourUTC {
		return fmt.Errorf("policy: control %q: time-window needs 0 <= startHourUtc < endHourUtc <= 24", id)
	}
	valid := map[string]bool{"sun": true, "mon": true, "tue": true, "wed": true, "thu": true, "fri": true, "sat": true}
	for _, d := range tw.Days {
		if !valid[d] {
			return fmt.Errorf("policy: control %q: unknown day %q (use sun..sat)", id, d)
		}
	}
	return nil
}

func firedMessage(c types.Control) string {
	if c.Type != "" {
		return fmt.Sprintf("control %q (%s) matched", c.ID, c.Type)
	}
	return fmt.Sprintf("control %q matched", c.ID)
}
