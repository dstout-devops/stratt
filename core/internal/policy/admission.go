package policy

import (
	"fmt"
	"time"

	"github.com/google/cel-go/cel"

	"github.com/dstout-devops/stratt/core/internal/rules"
	"github.com/dstout-devops/stratt/types"
)

// admissionEnv binds `object` — the declaration being admitted (ADR-0073),
// mirroring Kubernetes ValidatingAdmissionPolicy. Admission controls are CEL
// predicates over the manifest: `object.kind`, `object.spec.environment`,
// `object.labels.team`, etc. A tighter surface than the run env (only `object`);
// deeper builtin subsetting is a tracked hardening.
func admissionEnv() (*cel.Env, error) {
	return cel.NewEnv(cel.Variable("object", cel.DynType))
}

// admit evaluates admission controls (CEL over the declaration object) and
// combines them by the fixed most-restrictive lattice (ADR-0073). allow is the
// default; a fired deny rejects; an uncompilable or unevaluable control FAILS
// CLOSED to deny — never a silent admit (§1.8). This is the built-in CEL
// provider's admission implementation, reached only through the port.
func admit(controls []types.Control, object map[string]any) types.Decision {
	dec := types.Decision{
		Outcome:    types.OutcomeAllow,
		Provenance: types.DecisionProvenance{Engine: "cel-admission", EvaluatedAt: time.Now().UTC()},
	}
	env, err := admissionEnv()
	if err != nil {
		dec.Outcome = types.OutcomeDeny
		dec.Reasons = []types.Reason{{Code: "env_error", Message: err.Error()}}
		return dec
	}
	for _, c := range controls {
		prg, err := rules.CompileForEnv(env, c.When)
		if err != nil {
			dec.Outcome = mostRestrictive(dec.Outcome, types.OutcomeDeny)
			dec.Reasons = append(dec.Reasons, types.Reason{Code: "compile_error", Message: err.Error(), ControlID: c.ID})
			continue
		}
		ok, err := prg.EvalVars(map[string]any{"object": object})
		if err != nil {
			dec.Outcome = mostRestrictive(dec.Outcome, types.OutcomeDeny)
			dec.Reasons = append(dec.Reasons, types.Reason{Code: "eval_error", Message: err.Error(), ControlID: c.ID})
			continue
		}
		if !ok {
			continue
		}
		dec.Outcome = mostRestrictive(dec.Outcome, c.Outcome)
		dec.Reasons = append(dec.Reasons, types.Reason{Code: "fired", Message: firedMessage(c), ControlID: c.ID})
	}
	return dec
}

// ValidateAdmissionControls checks admission controls at declaration time
// (ADR-0073): each is a CEL `when` predicate over the declaration object with an
// allow or deny outcome ONLY — a declaration is admitted or rejected, never
// queued for approval (require_approval/escalate are the gate PEP's run-time
// job). Run-time typed primitives (TimeWindow/SoD/Waiver/BreakGlass) are not
// admission checks. Compiled against the admission env, fail-closed at load
// (§1.8). It is the CEL provider's admission-dialect validation.
func ValidateAdmissionControls(controls []types.Control) error {
	env, err := admissionEnv()
	if err != nil {
		return err
	}
	for _, c := range controls {
		if c.ID == "" {
			return fmt.Errorf("policy: admission control requires an id")
		}
		if c.When == "" {
			return fmt.Errorf("policy: admission control %q requires a `when` predicate", c.ID)
		}
		if c.TimeWindow != nil || c.SoD != nil || c.Waiver != nil || c.BreakGlass != nil {
			return fmt.Errorf("policy: admission control %q: run-time primitives (timeWindow/sod/waiver/breakGlass) are not admission checks", c.ID)
		}
		switch c.Outcome {
		case types.OutcomeAllow, types.OutcomeDeny:
		default:
			return fmt.Errorf("policy: admission control %q: outcome must be allow or deny (a declaration is admitted or rejected, ADR-0073)", c.ID)
		}
		if _, err := rules.CompileForEnv(env, c.When); err != nil {
			return fmt.Errorf("policy: admission control %q: %w", c.ID, err)
		}
	}
	return nil
}
