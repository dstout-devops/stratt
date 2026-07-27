package desiredstate

import (
	"fmt"
	"slices"
	"sort"

	"github.com/dstout-devops/stratt/types"
)

// ── capability-typed actuation (ADR-0140 D4) ─────────────────────────────────
//
// A Trigger or Baseline may name a capability CLASS instead of an Actuator, so the declaration
// says WHAT must converge and a provider swap edits no Trigger. `cert-reconcile` is the shape
// that matters: reconcile is the loop this platform never stops running, and it was the least
// capability-typed path in the estate.
//
// Which provider wins depends on the active environment and on which providers are VERIFIED —
// runtime state a Git tree cannot see. So everything checkable here is checked against EVERY
// CANDIDATE, never the one bound today. Same rule and the same words as checkBlueprintParamNames
// and checkProvisioningBuildInputs, applied to authority rather than to Workflow inputs.
//
// FacetWriteScope is the load-bearing one. It is the declaration's half of the write ceiling
// (ADR-0054: grant ∩ scope) and the grant belongs to the RESOLVED Actuator, so a scope that fits
// one provider's ceiling and exceeds another's is a write that silently stops happening on a
// rebind. A dropped write-back reports as NOTHING AT ALL — the reconcile converges the backend and
// the graph quietly stops being updated — which is why this cannot be left to launch.

// checkActuatorCapability validates every capability-typed actuation in the tree against all of
// its candidate providers.
func checkActuatorCapability(decls Declarations) error {
	for _, t := range decls.Triggers {
		if t.ActuatorCapability == "" {
			continue
		}
		what := fmt.Sprintf("trigger %s actuatorCapability %q", t.Name, t.ActuatorCapability)
		if err := checkCapabilityCandidates(decls, what, t.ActuatorCapability, t.FacetWriteScope, t.Params); err != nil {
			return err
		}
	}
	for _, b := range decls.Baselines {
		if b.ActuatorCapability == "" {
			continue
		}
		what := fmt.Sprintf("baseline %s actuatorCapability %q", b.Name, b.ActuatorCapability)
		if err := checkCapabilityCandidates(decls, what, b.ActuatorCapability, b.FacetWriteScope, b.Params); err != nil {
			return err
		}
	}
	return nil
}

// actuationCandidates lists every declared ACTUATOR advertising the class — the set a
// capability-typed actuation could resolve to.
//
// Actuators only: this form resolves to something DISPATCHABLE, and a Connector is not. A class
// provided solely by a Connector is therefore not actuation-shaped, and saying so at load beats
// resolving to nothing at launch.
//
// Deliberately NOT filtered by verification or environment, for the reason remediationCandidates
// records: this runs over a Git tree where neither is knowable, and a narrower set here would be a
// weaker check for no benefit.
func actuationCandidates(decls Declarations, capClass string) []types.Actuator {
	var out []types.Actuator
	for _, a := range decls.Actuators {
		if slices.Contains(a.Provides, capClass) {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func checkCapabilityCandidates(decls Declarations, what, capClass string, scope []string, params map[string]any) error {
	candidates := actuationCandidates(decls, capClass)
	if len(candidates) == 0 {
		// Refused at load rather than resolved to nothing at launch. A class no declared Actuator
		// provides is either a typo or a provider nobody admitted, and both are diff-time facts.
		return fmt.Errorf("%s: no declared Actuator provides this capability — an actuation resolves to a "+
			"DISPATCHABLE Actuator, so a class provided only by a Connector (or by nothing) can never bind (§1.8)", what)
	}
	for _, a := range candidates {
		// AUTHORITY. The effective write-back is this candidate's grant ∩ the declared scope, so a
		// namespace outside the grant is silently dropped at the one governor — never an error at
		// run time, just a Facet that stops being written.
		if bad := firstOutsideActuatorGrant(scope, a.FacetNamespaces); bad != "" {
			return fmt.Errorf("%s: facetWriteScope names %q, which candidate provider %q does not grant "+
				"(it grants %v). The effective write-back is grant ∩ scope (ADR-0054), so binding to this "+
				"provider would silently DROP that Facet rather than fail — narrow the scope, or widen the "+
				"provider's facetNamespaces", what, bad, a.Name, a.FacetNamespaces)
		}
		// INPUTS. There is no class-level Contract for an Actuator-shaped capability the way
		// capabilities/<class>.input exists for an Action-shaped one (ADR-0111/0112), and inventing
		// one no shipping Contract demands would violate §1.1. Checking the params against every
		// candidate's OWN input Contract achieves the same guarantee — the declaration is valid
		// whichever provider binds — and it surfaces a genuinely provider-shaped param set at the
		// diff instead of at a rebind. If a second, differently-shaped provider ever appears, that
		// failure is the signal that the class needs a real class-level Contract.
		if err := validateParamsContract(a.Name, params); err != nil {
			return fmt.Errorf("%s: params do not satisfy candidate provider %q: %w", what, a.Name, err)
		}
	}
	return nil
}

// firstOutsideActuatorGrant returns the first scope namespace absent from the grant, or "".
// Mirrors orchestrate.firstOutsideGrant, which enforces the same ceiling at dispatch (ADR-0054
// MF-2); this moves the discovery to the declaration for the capability-typed form, where the
// bound provider is not yet known.
func firstOutsideActuatorGrant(scope, grant []string) string {
	for _, ns := range scope {
		if !slices.Contains(grant, ns) {
			return ns
		}
	}
	return ""
}

// validateActuationTarget enforces the exclusivity of the two forms on one declaration. A
// declaration naming both has two answers to "what converges here", and a rule to choose between
// them is the implicit precedence §2.4 exists to refuse — the concrete name would silently win and
// every capability-typed declaration's behaviour would depend on whether someone left one behind.
func validateActuationTarget(what, actuator, capClass string) error {
	if actuator != "" && capClass != "" {
		return fmt.Errorf("%s: names an actuator and an actuatorCapability — one or the other, never both (§2.4)", what)
	}
	return nil
}
