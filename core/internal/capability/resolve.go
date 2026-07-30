// Package capability resolves an Intent's `requires: [provisioning]` (a capability CLASS, §1.5) to
// a concrete provider + build Action (ADR-0110 D3/D4). It is the reach-path the ADR-0107 D2
// follow-up specified: the Intent names the class, an operator estate binding selects the provider
// when >1 could serve, and the SOLE verified provider auto-binds. Resolution is PURE and fails
// CLOSED + observable (§1.8) — never a silent skip, never an implicit cross-provider fallback
// (§2.4). The caller supplies an immutable snapshot (verified providers + in-scope bindings); this
// package holds no state and reads no store, so it is trivially race-free.
package capability

import (
	"fmt"
	"sort"

	"github.com/dstout-devops/stratt/types"
)

// Provider is one VERIFIED provider of a capability class and the per-Intent-kind build Actions it
// advertises (its `provisions` map, ADR-0110 D3). The caller includes a provider here only if it is
// in the verified-provider index (ADR-0104 slice 2) and `provides` the class — a phantom/unverified
// provider must never be passed in (fail-closed is the caller's job to feed correctly).
type Provider struct {
	Name string // the provider declaration's name (an Actuator/Connector name)
	// Workflows maps an Intent kind (no "Intent/" prefix) to the Workflow THIS provider offers for
	// the class being resolved: its `provisions` for provisioning, its `decommissions` for teardown,
	// its `remediates` for remediation (ADR-0135 D2). Named for the shape rather than for the first
	// consumer, because a second one now populates it with a different map.
	Workflows map[string]string
	// Substrate is the landscape this provider builds in (ADR-0151 D1) — "aws", "kubernetes",
	// "vsphere", "vm". Empty for a provider that has not declared one, which is every provider
	// shipped before ADR-0151: such a provider is simply never selected BY substrate, and its
	// per-kind `provider:` bindings keep working unchanged.
	Substrate string
}

// Status is the outcome class of a resolution.
type Status int

const (
	// StatusResolved: a provider + build Action was bound. Provider/Action are set.
	StatusResolved Status = iota
	// StatusPending: fail-closed and OBSERVABLE (§1.8) — no verified provider builds this kind
	// (either none provides the class, or none advertises a build Action for the kind, ADR-0110 D4
	// both axes). The reconcile surfaces this as a pending Finding, never a silent no-op.
	StatusPending
	// StatusAmbiguous: >1 verified provider could build this kind and no estate binding selects one
	// — a §2.4 compile error (never a silent tiebreak). The operator adds a capability-binding.
	StatusAmbiguous
)

// Result is a resolution outcome. Reason is a human-readable message for the pending Finding or the
// compile error (§1.8 descent / §2.4 authoring error).
type Result struct {
	Status   Status
	Provider string
	Workflow string // the bound provider's gated build Workflow for the kind (ADR-0110 D3)
	Reason   string
}

// Resolve binds (capability, intentKind) to a provider + build Action over an immutable snapshot of
// the VERIFIED providers of that capability and the in-scope capability-bindings (ADR-0110 D3/D4).
//
//   - an in-scope binding entry for (capability, intentKind) selects the provider explicitly; the
//     named provider must be verified AND advertise a build Action for the kind, else PENDING;
//     conflicting entries naming DIFFERENT providers → AMBIGUOUS (§2.4);
//   - otherwise auto-bind: exactly one verified provider advertises the kind → RESOLVED;
//     zero → PENDING (fail-closed, both axes); ≥2 → AMBIGUOUS (add a binding, §2.4).
//
// intentKind is the bare kind (no "Intent/" prefix), matching a provider's `provisions` keys and a
// binding entry's intentKind. Inputs are never mutated.
func Resolve(capability, intentKind string, providers []Provider, bindings []types.CapabilityBinding) Result {
	// Which verified providers can build this kind (advertise a non-empty build Workflow for it).
	canBuild := map[string]string{}
	var builders []string
	for _, p := range providers {
		if wf := p.Workflows[intentKind]; wf != "" {
			canBuild[p.Name] = wf
			builders = append(builders, p.Name)
		}
	}
	sort.Strings(builders)

	// Explicit PROVIDER selection(s) for (capability, intentKind) across in-scope bindings.
	selected := map[string]bool{}
	// SUBSTRATE selection(s) (ADR-0151 D2): an entry with no intentKind covers every kind, and one
	// naming this kind covers just it.
	substrates := map[string]bool{}
	for _, b := range bindings {
		for _, e := range b.Entries {
			if e.Capability != capability {
				continue
			}
			switch {
			case e.Substrate != "" && (e.IntentKind == "" || e.IntentKind == intentKind):
				substrates[e.Substrate] = true
			case e.Provider != "" && e.IntentKind == intentKind:
				selected[e.Provider] = true
			}
		}
	}

	// A PER-KIND PROVIDER ENTRY WINS OVER A SUBSTRATE ENTRY, and this is the one precedence rule in
	// the resolver — declared, documented, and between two DIFFERENT selector forms rather than
	// between two values of one field. It is what makes ADR-0151 D2's tie-break work: when a
	// substrate legitimately offers two builders for a kind, the author names one for that kind and
	// leaves the substrate default in force for every other. Without it, adding the tie-break would
	// itself be a second candidate and the estate could never converge.
	//
	// It is NOT the implicit precedence §2.4 refuses — nothing is ranked by priority, recency or
	// declaration order, and two entries of the SAME form still conflict rather than pick a winner.
	// It is, however, the part of this design most in need of the charter-guardian review ADR-0151
	// records as owed; if the ruling goes the other way, the fix is to refuse the overlap and make
	// authors scope the substrate entry by kind.
	if len(selected) == 0 && len(substrates) > 0 {
		var wanted []string
		for sub := range substrates {
			wanted = append(wanted, sub)
		}
		sort.Strings(wanted)
		if len(substrates) > 1 {
			return Result{
				Status: StatusAmbiguous,
				Reason: fmt.Sprintf("conflicting capability-bindings select %d different substrates (%v) for Intent/%s (%s) — an environment builds on ONE substrate; resolve to one (§2.4, ADR-0151 D2)", len(wanted), wanted, intentKind, capability),
			}
		}
		var matched []string
		for _, p := range providers {
			if p.Substrate == wanted[0] && canBuild[p.Name] != "" {
				matched = append(matched, p.Name)
			}
		}
		sort.Strings(matched)
		switch len(matched) {
		case 1:
			return Result{Status: StatusResolved, Provider: matched[0], Workflow: canBuild[matched[0]]}
		case 0:
			// NAME WHAT WAS AVAILABLE, not merely that nothing matched (§1.8). "No provider builds
			// this kind" and "no provider OF THIS SUBSTRATE builds this kind" are different
			// problems with different fixes, and an operator who cannot tell them apart goes
			// looking in the wrong place.
			have := map[string]bool{}
			for _, p := range providers {
				if canBuild[p.Name] != "" && p.Substrate != "" {
					have[p.Substrate] = true
				}
			}
			var avail []string
			for sub := range have {
				avail = append(avail, sub)
			}
			sort.Strings(avail)
			return Result{
				Status: StatusPending,
				Reason: fmt.Sprintf("capability-binding selects substrate %q for Intent/%s (%s), but no verified provider of that substrate builds this kind (substrates that do: %v; builders: %v) — declare a provider with substrate: %s, or bind a provider by name for this kind (ADR-0151 D2)", wanted[0], intentKind, capability, avail, builders, wanted[0]),
			}
		default:
			return Result{
				Status: StatusAmbiguous,
				Reason: fmt.Sprintf("%d verified providers of substrate %q build Intent/%s (%v) — name one for this kind with a provider entry; picking for you would be the precedence §2.4 refuses (ADR-0151 D2)", len(matched), wanted[0], intentKind, matched),
			}
		}
	}

	switch len(selected) {
	case 1:
		var prov string
		for p := range selected {
			prov = p
		}
		wf, ok := canBuild[prov]
		if !ok {
			return Result{
				Status: StatusPending,
				Reason: fmt.Sprintf("capability-binding selects provider %q for Intent/%s (%s), but it is not a verified provider that builds this kind — check the provider is declared, verified, and its provisions[%s] is set (ADR-0110 D4)", prov, intentKind, capability, intentKind),
			}
		}
		return Result{Status: StatusResolved, Provider: prov, Workflow: wf}
	default:
		if len(selected) >= 2 {
			return Result{
				Status: StatusAmbiguous,
				Reason: fmt.Sprintf("conflicting capability-bindings select %d different providers for Intent/%s (%s) — resolve to one (§2.4)", len(selected), intentKind, capability),
			}
		}
	}

	// No explicit binding — auto-bind the sole verified builder.
	switch len(builders) {
	case 0:
		return Result{
			Status: StatusPending,
			Reason: fmt.Sprintf("no verified provider builds Intent/%s for capability %q — declare/verify a provider whose provisions cover %s, or add a capability-binding (ADR-0110 D4)", intentKind, capability, intentKind),
		}
	case 1:
		return Result{Status: StatusResolved, Provider: builders[0], Workflow: canBuild[builders[0]]}
	default:
		return Result{
			Status: StatusAmbiguous,
			Reason: fmt.Sprintf("%d verified providers build Intent/%s for capability %q (%v) — add a capability-binding to select one (§2.4, ADR-0110 D3)", len(builders), intentKind, capability, builders),
		}
	}
}
