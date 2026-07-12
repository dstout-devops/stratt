package types

import "encoding/json"

// Claim types (charter §2.4, the anti-GPO axiom): how a compiled Baseline
// claims the Facet namespace it manages. There is NO implicit precedence —
// exclusive double-claims fail compile; additive claims union.
const (
	// ClaimExclusive: one Assignment may claim the namespace per Entity; a
	// double-claim over overlapping Entities is a compile error.
	ClaimExclusive = "exclusive"
	// ClaimAdditive: set-union semantics (ensure-contains), for naturally
	// additive state (admin groups, trust stores).
	ClaimAdditive = "additive"
)

// Blueprint is a platform- or domain-owned composition that compiles
// (Intent × Assignment × View membership) into Baselines + remediation
// Workflow refs, routed by capability-scoped Facets (charter §2.4).
// Blueprints are versioned; Assignments pin a version. CaC-only.
type Blueprint struct {
	Name string `json:"name"`
	// Version is pinned by Assignments; upgrades roll through rings with
	// compile-diffs (§2.4).
	Version int `json:"version"`
	// For names the Intent kind this Blueprint composes (v1:
	// Intent/Application).
	For string `json:"for"`
	// Routes match Entities on capability-scoped Facets and declare, per
	// match, the observed Facet (with its claim type) and the remediation
	// Workflow ref.
	Routes []BlueprintRoute `json:"routes"`
	// Severity + DampingObservations stamp the compiled Baselines' Findings
	// (§4.3 flap damping).
	Severity            string `json:"severity"`
	DampingObservations int    `json:"dampingObservations,omitempty"`
}

// BlueprintRoute is one capability route: a Facet-predicate match → an
// observed Facet expectation (the compiled Baseline's check) + a
// remediation Workflow ref. Routing keys are per-capability Facets, never
// scalars — co-management is reality, not an edge case (§2.4).
type BlueprintRoute struct {
	// Match is the capability-scoped Facet predicates an Entity must satisfy
	// to be routed here (intersected with the Assignment's View membership).
	Match []FacetPredicate `json:"match,omitempty"`
	// Observe is the Facet this route's Baseline checks; its value may
	// reference the Intent spec by explicit field lookup ({{.spec.package}}).
	Observe FacetExpectation `json:"observe"`
	// Claim is how the observed namespace is claimed (exclusive|additive).
	Claim string `json:"claim"`
	// RemediationWorkflow names the declared Workflow that remediates this
	// route's Findings — a ref only, never auto-launched (§5 Flow 2). Same
	// field name as Baseline.RemediationWorkflow (one frozen concept, §2).
	RemediationWorkflow string `json:"remediationWorkflow,omitempty"`
}

// FacetExpectation is one check the compiled facet-observation Baseline
// evaluates graph-side against each targeted Entity (charter §2.4: "expected
// Facet values"). Exactly one of Equals / Contains is set.
type FacetExpectation struct {
	Namespace string `json:"namespace"`
	// Path is a dotted path within the Facet value ("" = whole value). It may
	// carry a {{.spec.X}} reference resolved from the Intent spec at compile.
	Path string `json:"path,omitempty"`
	// Equals asserts the addressed value equals this JSON value.
	Equals json.RawMessage `json:"equals,omitempty"`
	// Contains asserts the addressed value (an array or string) contains this
	// element (additive/ensure-contains semantics).
	Contains json.RawMessage `json:"contains,omitempty"`
}
