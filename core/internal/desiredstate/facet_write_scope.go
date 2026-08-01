package desiredstate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dstout-devops/stratt/types"
)

// checkFacetWriteScopeOwners refuses a declared `facetWriteScope` naming a Facet namespace that
// nothing in this estate will own (§2.1: registration precedes writes).
//
// ── THE THIRD INSTANCE OF ONE CLASS ─────────────────────────────────────────────────────────────
// This repo has now written the same check three times, each after the same failure:
//
//   - ADR-0145 — a provider advertised `provisions: {Subnet: opentofu-subnet-build}` against a
//     Workflow nobody had written. The build Finding named a Workflow an operator could not launch.
//
//   - CERT-1 — `remediates` had no such check, and a value mangled by an editing slip parsed
//     cleanly, passed the entire `task ci` gate, and was caught only by a running control plane.
//
//   - here — a Step's `facetWriteScope` names a namespace with no owner. Measured while building
//     demos/region-to-cert: `cert-issue`'s `deliver` Step scopes `fileset.content`, which has an
//     owner in the reference estate only because an unrelated `web-files` Assignment happens to
//     declare one. In an estate without it the Run dies with
//
//     facet namespace fileset.content has no registered owner
//     (charter §2.1: registration precedes writes)
//
//     AFTER a human has approved a build gate. The data-layer check is right; meeting it there is
//     the problem. Everything needed to answer the question is in Git.
//
// The shape is always the same — a declaration ADVERTISES something the estate cannot resolve —
// and the cost is always the same: the failure lands at the far end of an approved gate rather
// than in review.
//
// ── WHAT COUNTS AS AN OWNER, and why it is not simply "any facetNamespaces" ──────────────────────
// Ownership is registered at runtime from exactly three places, and this mirrors them rather than
// inventing a fourth rule:
//
//  1. A Blueprint route THAT REMEDIATES. `compiler.resolveOwnership` claims ownership only where
//     `route.remediationWorkflow != ""` — a pure observation reads a Facet (often Syncer-projected,
//     like os.kernel) and never seizes write-ownership.
//  2. A DIALLED plugin's grant. `pluginhost` registers `grant.FacetNamespaces` when it verifies a
//     Connector or Actuator it connects to. An EE-Job Actuator has no host and no grant, so its
//     `facetNamespaces` is a write CEILING and not a claim — which is exactly the case that makes a
//     naive "any facetNamespaces" version of this check useless: `ansible-certificate` declares
//     `facetNamespaces: [fileset.content, cert.presented]` and owns neither.
//  3. The namespaces core registers itself at boot (types.TeamOwned / ProjectorOwned).
//
// ── DELIBERATELY PERMISSIVE AT THE EDGES ────────────────────────────────────────────────────────
// A Blueprint route counts whether or not an Assignment currently binds it, and regardless of any
// `environments` filter. Runtime ownership needs a compiled Assignment in the active scope, so this
// accepts a few namespaces the running daemon would not yet own. That asymmetry is the right one:
// a false NEGATIVE here costs the diagnosis this check was written to improve, while a false
// POSITIVE would refuse an estate that works — turning a load-time guard into an outage. The
// motivating defect is caught either way, because the demo declared no such Blueprint at all.
func checkFacetWriteScopeOwners(decls Declarations) error {
	owned := map[string]bool{}
	for _, ns := range types.TeamOwnedFacetNamespaces() {
		owned[ns] = true
	}
	for _, ns := range types.ProjectorOwnedFacetNamespaces() {
		owned[ns] = true
	}
	for _, bp := range decls.Blueprints {
		for _, r := range bp.Routes {
			if r.RemediationWorkflow != "" && r.Observe.Namespace != "" {
				owned[r.Observe.Namespace] = true
			}
		}
	}
	// Dialled providers only — see the note above on why an EE-Job ceiling is not a claim.
	for _, a := range decls.Actuators {
		if a.Address == "" {
			continue
		}
		for _, ns := range a.FacetNamespaces {
			owned[ns] = true
		}
	}
	for _, c := range decls.Connectors {
		if c.Address == "" {
			continue
		}
		for _, ns := range c.FacetNamespaces {
			owned[ns] = true
		}
	}

	type site struct{ where, namespace string }
	var bad []site
	for _, w := range decls.Workflows {
		for _, s := range w.Steps {
			for _, ns := range s.FacetWriteScope {
				if !owned[ns] {
					bad = append(bad, site{fmt.Sprintf("workflow %q step %q", w.Name, s.Name), ns})
				}
			}
		}
	}
	for _, t := range decls.Triggers {
		for _, ns := range t.FacetWriteScope {
			if !owned[ns] {
				bad = append(bad, site{fmt.Sprintf("trigger %q", t.Name), ns})
			}
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Slice(bad, func(i, j int) bool {
		if bad[i].where != bad[j].where {
			return bad[i].where < bad[j].where
		}
		return bad[i].namespace < bad[j].namespace
	})

	var b strings.Builder
	for i, s := range bad {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "%s writes facet namespace %q", s.where, s.namespace)
	}
	// §1.8: name the cause AND the knobs. An operator meeting this needs to know that "no owner"
	// has three different fixes depending on which one they meant.
	return fmt.Errorf("%s — but nothing in this estate owns %s (charter §2.1: registration precedes "+
		"writes). A namespace is owned by a Blueprint route that REMEDIATES it, by a dialled "+
		"provider's facetNamespaces, or by core at boot. An EE-Job Actuator's facetNamespaces is a "+
		"write CEILING, not a claim — declaring it there does not make the write legal. Without this "+
		"check the Run fails at the far end of an approved gate",
		b.String(), map[bool]string{true: "it", false: "them"}[len(bad) == 1])
}
