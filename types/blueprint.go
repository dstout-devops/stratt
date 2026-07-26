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
	// Defaults are the base Intent-spec values for the composed kind (G6, ADR-0083
	// §5, ADR-0055 guardrail 6): the "sane defaults" an Assignment's Intent overrides.
	// Layered UNDER the Intent's own spec via explicit overlay merge
	// (core/internal/overlay) — the Blueprint is always the base, the Intent always
	// the override; there is NO precedence field (§2.4/§4.1 anti-GPO). A field the
	// Intent omits takes the default; a field it sets overrides, traceably. Referenced
	// by routes via {{.spec.X}} exactly as a directly-declared spec value is.
	Defaults map[string]any `json:"defaults,omitempty"`
	// Routes match Entities on capability-scoped Facets and declare, per
	// match, the observed Facet (with its claim type) and the remediation
	// Workflow ref.
	Routes []BlueprintRoute `json:"routes"`
	// Severity + DampingObservations stamp the compiled Baselines' Findings
	// (§4.3 flap damping).
	Severity            string `json:"severity"`
	DampingObservations int    `json:"dampingObservations,omitempty"`
	// RemoveWorkflow is the Workflow ref surfaced on the orphan Finding when an
	// Intent of this kind is withdrawn with onRemove: remove (§2.4, ADR-0030) —
	// e.g. a certificate revoke Workflow. A ref only: the operator launches it
	// (§5 Flow 2), never auto-run. Empty ⇒ withdrawal is retain-only.
	RemoveWorkflow string `json:"removeWorkflow,omitempty"`
	// RemoveParams are the values passed to RemoveWorkflow, {{.spec.X}}-substituted from the
	// resolved spec at compile and CARRIED ONTO THE COMPILED BASELINE — the withdrawal half of
	// the parameter plane (ADR-0118 D3).
	//
	// Blueprint-level rather than per-route, because RemoveWorkflow is: withdrawal retires the
	// Assignment's whole compiled set, not one route's expectation.
	//
	// WHY THE COMPILED VALUES MUST BE STORED, and not recomputed at withdrawal like
	// RemoveWorkflow is: the resolved spec is Blueprint defaults + Intent spec + ASSIGNMENT
	// VALUES, and withdrawal means the Assignment is gone from Git. Its values cannot be read
	// back at the moment they are needed. So the ref — a property of the pinned Blueprint,
	// still declared — is read then, and the params — a property of the compiled
	// instantiation, unrecoverable — are stamped on the Baseline at compile. Storing the ref
	// too would give one fact two authorities (§1.2); storing only what cannot be recovered is
	// the line.
	//
	// Validated against RemoveWorkflow's declared input schema at compile, exactly as
	// RemediationParams is: a withdrawal Workflow wired to params it does not fit must fail in
	// front of the author, not in front of the operator holding an orphan Finding (§1.8).
	RemoveParams map[string]any `json:"removeParams,omitempty"`
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
	//
	// KEPT, not deprecated (ADR-0135 D3): an estate that has decided which provider
	// converges its hosts is clearer naming the Workflow than routing through an
	// indirection that resolves to one answer. Mutually exclusive with
	// RemediationCapability below — project/ has one remediation leg, and a merge
	// between the two would need a winner (§2.4, no implicit precedence).
	RemediationWorkflow string `json:"remediationWorkflow,omitempty"`
	// RemediationCapability names a capability CLASS whose bound provider supplies this
	// route's remediation Workflow (ADR-0135 D3) — `configmgmt` for host convergence.
	// Resolved AT COMPILE through the same capability.Resolve that binds provisioning,
	// against the verified in-environment providers and the estate's capability-bindings.
	//
	// This is the last name-bound edge of the intent layer. A route naming a Workflow
	// names an Actuator names a content project, so a Blueprint carrying one cannot be
	// shared — which is the whole reason a plugin cannot usefully ship examples
	// (ADR-0135 D1). Naming the class instead leaves every field of a Blueprint
	// provider-agnostic and lets the ESTATE supply the binding.
	//
	// The compiled Baseline still carries a CONCRETE Workflow: resolution happens once,
	// at compile, so descent shows one answer and a rebind recompiles rather than
	// silently changing what a Finding offers (§1.8).
	RemediationCapability string `json:"remediationCapability,omitempty"`
	// RemediationParams are the values this route passes to its RemediationWorkflow,
	// {{.spec.X}}-substituted from the resolved spec at compile and carried onto the
	// compiled Baseline (ADR-0118 D3).
	//
	// This field is the whole reason the parameter plane used to stop at the observation
	// boundary. A route named the Workflow that converges the estate and passed it NOTHING,
	// so every remediation Workflow had to re-declare by hand what its Intent already said —
	// which is why `port: "443"` appeared three times in the app-cert demo.
	//
	// Validated against the named Workflow's declared input schema AT COMPILE: an unknown key
	// or a missing required input fails the Assignment, so a route wired to a Workflow it
	// does not fit breaks in front of the person editing the declaration rather than in front
	// of the operator at 3am (§1.8). Same field name as Baseline.RemediationParams — one
	// concept, one name (§2), the rule already applied to RemediationWorkflow above.
	RemediationParams map[string]any `json:"remediationParams,omitempty"`
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
	// NotBefore asserts the addressed value (an RFC3339 timestamp) is at least
	// this Go duration (e.g. "360h") in the FUTURE at evaluation time — the
	// Baseline-side expiry threshold (ADR-0030): cert.expiry.notAfter must be
	// at least renewBefore ahead, else the cert drifts toward expiry. The
	// window is Git policy, sourced from the Intent spec ({{.spec.renewBefore}})
	// and substituted at compile. Empty ⇒ unused. Exactly one of
	// Equals/Contains/NotBefore is set.
	NotBefore string `json:"notBefore,omitempty"`
}
