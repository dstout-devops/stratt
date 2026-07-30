package desiredstate

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/dstout-devops/stratt/types"
)

// checkNothingAboveAProviderNamesASubstrate is ADR-0151 follow-up 4 — "a lint that fails any
// Intent/Blueprint/Assignment/View naming a substrate: the kube-app guard".
//
// ADR-0151 D1/D2 made the substrate a property of the PROVIDER, selected by ONE line in a
// capability-binding, so that changing `substrate: kubernetes` to `substrate: aws` migrates a
// whole topology and nothing above that line moves with it. That property is only real if
// nothing above the line quietly names a landscape — a single `spec: {substrate: kubernetes}`
// or `params: {provider: kubecompute}` re-couples the declaration, the binding edit stops
// meaning what it says, and the estate ends up half on one substrate and half on the other:
// the exact outcome D2's resolver refuses when it can SEE the contest.
//
// It could not see this one, because a free-form `spec`/`params`/`values` map is opaque to
// core (§1.5) and nothing typed it.
//
// TWO RULES, both keyed on a FIELD NAME rather than on a value, because that is the part
// that is unambiguous:
//
//  1. A key named `substrate` is refused outright. Exactly two declarations may carry that
//     field — a provider's own (actuators/, connectors/) and a capability-binding entry — and
//     neither is checked here.
//  2. A key named `provider` is refused when its value names a declared provider or a
//     substrate token. `provider:` is legal in a capability-binding and nowhere above it
//     (ADR-0106 D1: a route names a capability, never a provider).
//
// WHAT IT DELIBERATELY DOES NOT DO is refuse a substrate token as a bare VALUE, or in a
// declaration's NAME. `ami: ami-0aws-baseline` and an Intent called `aws-billing-fleet` are
// prose — they route nothing, and a lint that fired on them would be a spell-checker with a
// veto. The rule is about which FIELD decides where a thing gets built.
//
// A View selector IS linted, and that is not obvious: a selector reads OBSERVED labels, and
// selecting on an observed fact is ordinarily legitimate (§1.2). But `labels: {substrate:
// kubernetes}` makes the View's MEMBERSHIP substrate-specific, so the binding line no longer
// migrates it — the Assignment keeps pointing at a set defined by the landscape it used to be
// on. That is the coupling this lint exists for, arriving through the one door that looks like
// an observation.
func checkNothingAboveAProviderNamesASubstrate(decls Declarations) error {
	// Providers as declared, so `provider: kubecompute` is caught by the name the estate
	// actually uses rather than by a list maintained here.
	providers := map[string]bool{}
	for _, a := range decls.Actuators {
		providers[a.Name] = true
	}
	for _, c := range decls.Connectors {
		providers[c.Name] = true
	}
	subs := types.Substrates()

	// bad reports the first offending path in a declared value tree, or "".
	var bad func(path string, v any) string
	bad = func(path string, v any) string {
		switch t := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys) // deterministic: the same estate must fail on the same field
			for _, k := range keys {
				at := k
				if path != "" {
					at = path + "." + k
				}
				if strings.EqualFold(k, "substrate") {
					return at
				}
				if strings.EqualFold(k, "provider") {
					if s, ok := t[k].(string); ok && (providers[s] || slices.Contains(subs, s)) {
						return at
					}
				}
				if p := bad(at, t[k]); p != "" {
					return p
				}
			}
		case []any:
			for i, e := range t {
				if p := bad(fmt.Sprintf("%s[%d]", path, i), e); p != "" {
					return p
				}
			}
		}
		return ""
	}

	refuse := func(kind, name, at string) error {
		return fmt.Errorf(
			"%s %s declares %q — nothing above a provider may name a substrate or a provider "+
				"(ADR-0151 D1/D2). The substrate is a property of the PROVIDER, selected by one line "+
				"in a capability-binding, and that line only migrates a topology if no declaration "+
				"above it is separately coupled to one. Name a capability CLASS instead, and let "+
				"estate/capability-bindings/ choose (§1.5, ADR-0106 D1)", kind, name, at)
	}

	for _, in := range decls.Intents {
		if at := bad("spec", in.Spec); at != "" {
			return refuse("intent", in.Name, at)
		}
	}
	for _, b := range decls.Blueprints {
		if at := bad("defaults", b.Defaults); at != "" {
			return refuse("blueprint", b.Name, at)
		}
		for i, r := range b.Routes {
			if at := bad(fmt.Sprintf("routes[%d].remediationParams", i), r.RemediationParams); at != "" {
				return refuse("blueprint", b.Name, at)
			}
		}
	}
	for _, a := range decls.Assignments {
		if at := bad("values", a.Values); at != "" {
			return refuse("assignment", a.Name, at)
		}
	}
	for _, v := range decls.Views {
		labels := make(map[string]any, len(v.Selector.Labels))
		for k, val := range v.Selector.Labels {
			labels[k] = val
		}
		if at := bad("selector.labels", labels); at != "" {
			return refuse("view", v.Name, at)
		}
	}
	return nil
}
