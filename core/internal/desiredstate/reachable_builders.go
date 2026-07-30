package desiredstate

import (
	"sort"

	"github.com/dstout-devops/stratt/core/internal/capability"
	"github.com/dstout-devops/stratt/types"
)

// reachableBuilders returns the build (or teardown) Workflows that could actually run for this
// Intent — the candidate set checkProvisioningBuildInputs validates it against.
//
// It answers one question the old code could not ask: WHERE is this Intent reconciled? Before
// types.Intent carried an `environments` filter the answer was always "everywhere", so the
// candidate set was the union of every declared provider's builder for the kind and an
// Intent/Compute had to satisfy every substrate at once. See types.Intent.Environments for what
// that cost the shipped estate — which is still paying it, because scoping the reference estate
// is a separate change that has to be verified by running the demos.
//
// Three deliberate asymmetries with the runtime resolver, all in the conservative direction:
//
//  1. EVERY DECLARED PROVIDER IS TREATED AS VERIFIED. Verification is runtime state Git cannot see
//     (ADR-0110 D3), and guessing it would make the check weaker than the reconcile. Treating them
//     all as verified can only WIDEN the candidate set, never narrow it.
//
//  2. AN UNRESOLVED BINDING WIDENS RATHER THAN REFUSING. If capability.Resolve reports ambiguity or
//     no binding at all for this kind in an environment, every in-scope provider that builds the
//     kind stays a candidate. The ambiguity itself is the resolver's business to refuse at
//     reconcile — this function's only job is "which Workflows must accept core's params", and the
//     safe answer to "we don't know yet" is "all of them".
//
//  3. THE UNSCOPED DAEMON IS ALWAYS SIMULATED, on top of whatever environments the Intent names.
//     This one was MISSING and it is the one that mattered: `STRATT_ENVIRONMENT` empty is the
//     default and what every demo estate runs, and there `types.InScope` is true for everything —
//     including providers and bindings scoped to environments the Intent is not in. An Intent
//     scoped `[dev]` whose only provider is scoped `[prod]` produced an EMPTY candidate set, so
//     nothing was checked at all, while an unscoped daemon resolved that provider's builder
//     happily and failed at launch after an operator approved the gate. That is precisely the
//     §1.8 regression app-tier's own comment records as having been fixed by moving the check to
//     load, reintroduced through a door nobody was watching.
//
//     Only its RESOLVED outcome contributes: at env "" an unresolved result means the reconcile
//     fails closed and launches nothing, so widening there would restore the whole union and undo
//     the change for no safety gained.
//
// WHAT THIS IS NOT: it is not "the union an unscoped Intent always got". The candidate set is the
// union of the PER-ENVIRONMENT RESOLUTIONS, which is narrower than the old union whenever a
// binding resolves — including for an unscoped Intent. That narrowing is the point; the claim to
// check it against is soundness (every builder the reconcile could actually reach is in the set),
// not size.
func reachableBuilders(decls Declarations, in types.Intent, kind string, teardown bool) []string {
	envs := in.Environments
	if len(envs) == 0 {
		for _, e := range decls.Environments {
			envs = append(envs, e.Name)
		}
	}

	// Verification is not knowable from Git, so every declared provider counts (see above).
	verified := make(map[string]bool, len(decls.Actuators)+len(decls.Connectors))
	for _, a := range decls.Actuators {
		verified["actuator/"+a.Name] = true
	}
	for _, c := range decls.Connectors {
		verified["connector/"+c.Name] = true
	}

	seen := map[string]bool{}
	var out []string
	keep := func(wf string) {
		if wf != "" && !seen[wf] {
			seen[wf] = true
			out = append(out, wf)
		}
	}

	resolveIn := func(env string) (capability.Result, []capability.Provider) {
		providers := assembleProvisioningProviders(
			verified, decls.Actuators, decls.Connectors, env, types.CapProvisioning)
		if teardown {
			providers = assembleTeardownProviders(verified, decls.Actuators, decls.Connectors, env)
		}
		return capability.Resolve(
			types.CapProvisioning, kind, providers, inScopeBindings(decls.CapabilityBindings, env)), providers
	}

	for _, env := range envs {
		res, providers := resolveIn(env)
		if res.Status == capability.StatusResolved {
			keep(res.Workflow)
			continue
		}
		for _, p := range providers {
			keep(p.Workflows[kind])
		}
	}
	// Asymmetry 3: the unscoped daemon, always, resolved-only. See the doc comment.
	if res, _ := resolveIn(""); res.Status == capability.StatusResolved {
		keep(res.Workflow)
	}
	sort.Strings(out) // deterministic: the same estate must fail on the same Workflow
	return out
}
