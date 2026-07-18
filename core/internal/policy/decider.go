package policy

import (
	"context"

	"github.com/dstout-devops/stratt/types"
)

// Decider is the Policy Decision Point PORT (ADR-0072): the seam through which
// the content-blind core obtains a governance Decision, exactly as
// authz.Authorizer is the seam for authorization and the Actuator/Action
// registries are the seam for infrastructure. The core PEP (the policy Step,
// the gate, audit, the mandatory floors) calls this port; it never calls a
// concrete engine. Providers are swappable and bypassable:
//   - CEL (below) — the built-in, zero-dependency in-tree provider.
//   - an external engine (OPA/Cerbos/…) over the sovereign gRPC port — a plugin.
//   - Bypass — policy disabled, recorded, never silent.
//
// Governance domain semantics (SoD/TimeWindow/Waiver/BreakGlass, the lattice)
// live in a PROVIDER, never in the core call path — the spine stays content-blind
// (§1.1/§1.4, ADR-0046). A Decider must never return an outcome that GRANTS more
// than the launch-time authz allowed; it can only add restriction (§1.6, M2).
type Decider interface {
	Decide(ctx context.Context, req Request) types.Decision
	// Validate checks a provider's policy declarations at load — fail the file,
	// never silently at decision time (§1.8). Each provider validates its own
	// dialect (the CEL provider validates inline typed Controls; a future OPA
	// provider would validate its bundles), so declaration-time validation is
	// engine-selected through the port, never hardcoded to one engine
	// (charter-guardian follow-up on ADR-0072).
	Validate(controls []types.Control) error
}

// Request is the port input: the controls to apply and the change being judged.
// Inline controls travel with the request (they are DATA on the policy Step); an
// external engine may instead consult its own loaded policy and treat Controls
// as advisory — the port abstracts that choice from the core.
type Request struct {
	Controls []types.Control
	Context  types.ChangeContext
}

// CEL is the built-in in-tree provider (ADR-0072): it evaluates the controls
// with the hermetic CEL engine + the typed Control library. It is one provider
// among many, selected by configuration — the default, never load-bearing (the
// core runs without it via Bypass or a swapped engine).
type CEL struct{}

func (CEL) Decide(_ context.Context, req Request) types.Decision {
	return Evaluate(req.Controls, req.Context)
}

// Validate is the CEL provider's declaration-time check of its dialect — inline
// typed Controls (ADR-0067/0068/0069/0070).
func (CEL) Validate(controls []types.Control) error { return ValidateControls(controls) }

// Bypass disables policy explicitly and VISIBLY (§1.8: never a silent skip). It
// allows every change but stamps a "policy-bypassed" reason and a bypass engine,
// so the audit stream (ADR-0065) shows governance was turned off — the honest
// realisation of "we must be able to bypass every external tool".
type Bypass struct{}

func (Bypass) Decide(_ context.Context, _ Request) types.Decision {
	return types.Decision{
		Outcome:    types.OutcomeAllow,
		Reasons:    []types.Reason{{Code: "policy-bypassed", Message: "policy decision point bypassed by configuration"}},
		Provenance: types.DecisionProvenance{Engine: "bypass"},
	}
}

// Validate is a no-op when policy is bypassed — a disabled engine validates
// nothing (consistent with Bypass allowing every decision).
func (Bypass) Validate([]types.Control) error { return nil }
