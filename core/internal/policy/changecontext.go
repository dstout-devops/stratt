package policy

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dstout-devops/stratt/types"
)

// Change-context admission (ADR-0122). Two questions, and the split between them is the whole
// decision: which facts about a change may the LAUNCHER assert, and which must CORE establish?
//
// Typing the asserted ones without answering that leaves a launcher free to assert a fact core
// already knows — which is not a data-quality problem but an authorization one. So the asserted set
// is validated (D1) and the derivable set is refused outright (D2/D3), never merged with precedence:
// there is no conflict to resolve because the conflicting assertion cannot be made.

// ValidateChangeContext admits what a launcher asserts about a change, or refuses it by name.
//
// Called at ADR-0118 D4's chokepoint below all four launch paths AND eagerly at the API door, the
// same two-call-site shape ResolveLaunchInputs uses: one call for a good error message, one that no
// transport can skip (§1.6).
func ValidateChangeContext(supplied map[string]any) error {
	for _, k := range sortedKeys(supplied) {
		switch {
		case k == types.ChangeContextEnvironmentKey:
			// Environment is a property of the FLOOR, not of a request (ADR-0057: a daemon
			// carries an active environment and reconciles only its slice). Core stamps it.
			//
			// Refused rather than overwritten, because the interesting failure is not a typo:
			// before this, a caller on a prod floor could assert `environment: dev` and walk
			// past a prod freeze window. Typing the string would not have fixed that — only
			// taking the field away does (§2.5).
			return fmt.Errorf(
				"change context may not assert %q: the environment is a property of the floor this Run "+
					"executes on (ADR-0057), so core stamps it from the daemon's own active environment. "+
					"A launcher choosing its own policy environment could walk past an environment-keyed "+
					"Control (ADR-0122 D2)", k)
		case strings.HasPrefix(k, types.ChangeLabelPrefix):
			// The reserved namespace: core-derived facts must not share the launcher's label bag
			// on equal terms, or they are spoofable. Same guard shape ADR-0120 uses to keep
			// `stratt.intent/` core's own.
			return fmt.Errorf(
				"change context may not carry %q: the %s namespace is core's, and every key in it is "+
					"DERIVED from the declaration rather than asserted — a launcher able to set one could "+
					"assert its way out of a Control that gates on it (§2, ADR-0122 D3)",
				k, types.ChangeLabelPrefix)
		case k == types.ChangeContextClassKey:
			cls, ok := supplied[k].(string)
			if !ok {
				return fmt.Errorf("change context %q must be a string, got %T", k, supplied[k])
			}
			if !types.ValidChangeClass(cls) {
				// Refused, never coerced. An unknown class means the Controls keyed on the
				// intended one do not fire, and the change proceeds — fail-OPEN, which is why
				// this is the one that had to be closed (§1.8, ADR-0122 D1).
				return fmt.Errorf(
					"change context %q=%q is not a known change class (want one of %v) — an unknown class "+
						"makes every Control keyed on the intended one silently not fire, and the change "+
						"proceeds (ADR-0122 D1)", k, cls, types.ChangeClasses)
			}
		}
	}
	return nil
}

// DeriveElevation reports the core-owned labels a Run's DECLARATION earns it, given each Step's
// params and the elevating input paths its Actuator declares (ADR-0122 D3).
//
// This is what makes ADR-0117 D1's booked gating possible without `if ansible{}`: core reads a
// declared path list and a truthy value at that path. It never learns the word `become` — the same
// content-blind shape as `facetNamespaces`, where core enforces a namespace ceiling without knowing
// what `os.hardening.sshd` means.
//
// elevatedBy maps Actuator name → the paths that Actuator declares as elevating. A Step whose
// Actuator declares none contributes nothing, which is honest and visible in review rather than
// automatic.
func DeriveElevation(steps []types.Step, elevatedBy map[string][]string) map[string]string {
	for _, st := range steps {
		for _, path := range elevatedBy[st.Actuator] {
			if truthyAtPath(st.Params, path) {
				// One class, not a taxonomy (ADR-0122 D3): `privileged` is the only derived
				// class because it is the only one a shipping Control needs. A second argues
				// membership.
				return map[string]string{types.ChangeLabelPrivileged: "true"}
			}
		}
	}
	return nil
}

// truthyAtPath walks a dotted path through a params map and reports whether it holds a value that
// means "yes". Deliberately narrow: a bool true, or the string "true". A number or a non-empty
// string is NOT treated as truthy, because guessing would make the gate fire on values the plugin
// never meant as an enable — and a gate that fires wrongly gets routed around.
func truthyAtPath(params map[string]any, path string) bool {
	cur := any(params)
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		cur, ok = m[seg]
		if !ok {
			return false
		}
	}
	switch v := cur.(type) {
	case bool:
		return v
	case string:
		return v == "true"
	default:
		return false
	}
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out) // deterministic refusal order — the first offender is stable across runs
	return out
}
