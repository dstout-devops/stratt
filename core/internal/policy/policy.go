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

	var fired []types.Control
	for _, c := range controls {
		prg, err := rules.CompileForEnv(env, c.When)
		if err != nil {
			dec.Outcome = mostRestrictive(dec.Outcome, types.OutcomeDeny)
			dec.Reasons = append(dec.Reasons, types.Reason{
				Code: "compile_error", Message: err.Error(), ControlID: c.ID,
			})
			continue
		}
		ok, err := prg.EvalVars(map[string]any{"ctx": ctxMap(cc)})
		if err != nil {
			// Fail-closed: a predicate that cannot evaluate (including a
			// reference to an absent optional risk/criticality coordinate)
			// denies — most-restrictive, never "no risk" (ADR-0061 M4).
			dec.Outcome = mostRestrictive(dec.Outcome, types.OutcomeDeny)
			dec.Reasons = append(dec.Reasons, types.Reason{
				Code: "eval_error", Message: err.Error(), ControlID: c.ID,
			})
			continue
		}
		if !ok {
			continue // predicate false: this control does not fire
		}
		fired = append(fired, c)
		dec.Outcome = mostRestrictive(dec.Outcome, c.Outcome)
		dec.Reasons = append(dec.Reasons, types.Reason{
			Code: "fired", Message: firedMessage(c), ControlID: c.ID,
		})
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

func firedMessage(c types.Control) string {
	if c.Type != "" {
		return fmt.Sprintf("control %q (%s) matched", c.ID, c.Type)
	}
	return fmt.Sprintf("control %q matched", c.ID)
}
