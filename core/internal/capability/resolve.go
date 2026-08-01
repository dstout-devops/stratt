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
	substrateClaims := false
	for _, b := range bindings {
		for _, e := range b.Entries {
			if e.Capability != capability {
				continue
			}
			switch {
			case e.Substrate != "" && (e.IntentKind == "" || e.IntentKind == intentKind):
				// CLAIMED, whether or not any provider of it can build this kind. An entry with no
				// intentKind claims EVERY kind — so "the substrate cannot serve this kind" is a
				// refusal to diagnose, never an opening for another substrate's provider to fill.
				substrates[e.Substrate] = true
				substrateClaims = true
			case e.Provider != "" && e.IntentKind == intentKind:
				selected[e.Provider] = true
			}
		}
	}

	// COMBINING THE TWO SELECTOR FORMS (ADR-0151 D2, as ruled by charter-guardian 2026-07-30).
	//
	// The first version of this made a per-kind `provider:` entry WIN over a substrate entry, on the
	// argument that a specificity rule between two different selector FORMS is not the implicit
	// precedence §2.4 forbids. That was wrong on three counts and the third is the tell: §2.4 is
	// named the anti-GPO axiom, and GPO precedence IS a ranking among differently-scoped declaration
	// forms; ADR-0118 D1 already refused "last explicit layer wins" even with a declared, documented
	// order, admitting a layer to yield only where it is DEFINITIONALLY UNSET; and this resolver
	// already refuses substrate-vs-substrate and provider-vs-provider contests rather than ranking
	// by specificity, so the old rule was the single exception in a design that otherwise agrees
	// specificity must not decide.
	//
	// It was not a theoretical failure. The shipped dev estate — `provisioning-kube` (substrate:
	// kubernetes, every kind) beside `provisioning-subnet` (provider: opentofu-network, which
	// declares substrate: aws) — resolved Compute to kubecompute and Subnet to opentofu-network,
	// BOTH green, with no diagnosis: a silently half-Kubernetes, half-AWS topology in the
	// environment whose binding says "this environment is Kubernetes". That is verbatim the defect
	// this ADR was written to eliminate, reproduced by the rule meant to prevent it.
	//
	// The rule now: the forms COMBINE only where the substrate entry is UNDERDETERMINED on this
	// kind, never where they contest it — ADR-0118 D1's shape exactly.
	if substrateClaims {
		var wanted []string
		for sub := range substrates {
			wanted = append(wanted, sub)
		}
		sort.Strings(wanted)
		if len(wanted) > 1 {
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
		inSubstrate := map[string]bool{}
		for _, m := range matched {
			inSubstrate[m] = true
		}

		if len(selected) > 0 {
			// A provider entry may only COMPLETE an underdetermined substrate — it must name a
			// provider OF that substrate, and the substrate must have left the choice open.
			var picked []string
			for p := range selected {
				picked = append(picked, p)
			}
			sort.Strings(picked)
			outside := false
			for _, p := range picked {
				if !inSubstrate[p] {
					outside = true
				}
			}
			switch {
			case outside:
				return Result{
					Status: StatusAmbiguous,
					Reason: fmt.Sprintf("capability-binding selects substrate %q for Intent/%s (%s), and another selects provider(s) %v which are NOT of that substrate (providers of %q that build this kind: %v) — this is how a topology ends up silently half on one substrate and half on another. Declare ONE: change the provider entry, or scope the substrate entry so it does not claim this kind (§2.4, ADR-0151 D2)", wanted[0], intentKind, capability, picked, wanted[0], matched),
				}
			case len(matched) == 1:
				return Result{
					Status: StatusAmbiguous,
					Reason: fmt.Sprintf("substrate %q already resolves Intent/%s (%s) to %q, and a provider entry also selects %v — two bindings answering one question is a contest, not a refinement, and ranking them would be the precedence §2.4 refuses. Remove one (ADR-0151 D2)", wanted[0], intentKind, capability, matched[0], picked),
				}
			case len(picked) > 1:
				return Result{
					Status: StatusAmbiguous,
					Reason: fmt.Sprintf("provider entries select %v for Intent/%s (%s) — resolve to one (§2.4)", picked, intentKind, capability),
				}
			default:
				// UNDERDETERMINED and COMPLETED: the substrate offers >1 builder for this kind and
				// the provider entry names one OF that substrate. The forms are not contesting —
				// the substrate left the choice open and the author closed it (ADR-0118 D1's
				// "a layer may yield only where it is definitionally unset").
				return Result{Status: StatusResolved, Provider: picked[0], Workflow: canBuild[picked[0]]}
			}
		}

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
				Reason: fmt.Sprintf("capability-binding selects substrate %q for Intent/%s (%s), but no verified provider of that substrate builds this kind (substrates that do: %v; builders: %v) — declare a provider with substrate: %s, or scope the substrate entry so it does not claim this kind (ADR-0151 D2)", wanted[0], intentKind, capability, avail, builders, wanted[0]),
			}
		default:
			return Result{
				Status: StatusAmbiguous,
				Reason: fmt.Sprintf("%d verified providers of substrate %q build Intent/%s (%v) — name one for this kind with a provider entry; picking for you would be the precedence §2.4 refuses (ADR-0151 D2)", len(matched), wanted[0], intentKind, matched),
			}
		}
	}

	// No substrate entry claims this kind — the pre-ADR-0151 path, unchanged: an explicit provider
	// selection binds, and conflicting provider entries are a §2.4 error.
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
