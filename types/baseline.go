package types

// Finding severities, declared on the Baseline and stamped onto every
// Finding it raises.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// Baseline is desired state made checkable (charter §2.4): a View selector +
// a check Step + an optional remediation Workflow ref + a cadence. v1 ships
// the hand-written rung of the §6 ladder — the Intent/Blueprint compiler
// (Phase 2, separate slice) emits the compiled kind into the same object.
//
// A Baseline's check must never mutate the estate: the platform enforces
// read-only execution per Actuator (opentofu mode=plan; ansible check mode),
// not by convention. Baselines are CaC-only; Principal names the service
// identity the checks execute as — the ADR-0010 impersonation-via-Git
// posture, unchanged.
type Baseline struct {
	Name string `json:"name"`
	// Check Step: which View to check, with which Actuator and params.
	ViewName       string         `json:"viewName"`
	Actuator       string         `json:"actuator"`
	Params         map[string]any `json:"params,omitempty"`
	Slices         int            `json:"slices,omitempty"`
	CredentialRefs []string       `json:"credentialRefs,omitempty"`
	Principal      string         `json:"principal,omitempty"`
	// FacetWriteScope is the Facet namespaces this check may write back (ADR-0054):
	// the effective write-back allowlist is the actuator's grant ∩ this scope. Empty
	// admits NO facet write-back (TIGHT least-authority default).
	FacetWriteScope []string `json:"facetWriteScope,omitempty"`
	// Cron is the check cadence (a Temporal Schedule actuates it, §3:
	// "Baseline cadences").
	Cron string `json:"cron"`
	// Paused declares the cadence paused without deleting it.
	Paused bool `json:"paused,omitempty"`
	// Severity stamps the Findings this Baseline raises (info|warning|
	// critical).
	Severity string `json:"severity"`
	// DampingObservations is the flap-damping N (charter §4.3): a Finding
	// opens only after N consecutive drifted observations. 0 means 1.
	DampingObservations int `json:"dampingObservations,omitempty"`
	// RemediationWorkflow names the declared Workflow that remediates this
	// Baseline's Findings. A ref only — remediation is never auto-launched
	// (§5 Flow 2: remediation behind Gates).
	RemediationWorkflow string `json:"remediationWorkflow,omitempty"`
	// Framework tags the Findings (e.g. "cis") — "one kind, framework-
	// tagged" (§2.4).
	Framework string `json:"framework,omitempty"`
	// Environments scopes this Baseline to a subset of environments (ADR-0057);
	// empty = all. A compiled Baseline inherits its source Assignment's set so a
	// dev-compiled Baseline is invisible to a prod daemon's prune.
	Environments []string `json:"environments,omitempty"`

	// ── facet-observation variant (compiler output, ADR-0023) ──────────────
	// Mode selects the check machinery: "" (default) is a check Step (the
	// ADR-0019 ansible/tofu Run); "facet-observation" is evaluated
	// graph-side against the compiled Selector — the desired state is
	// "these Entities should carry this Facet value" (§2.4 "expected Facet
	// values"), no execution pod.
	Mode string `json:"mode,omitempty"`
	// Selector is the compiled target set (the Assignment's View selector ∩
	// the Blueprint route match). facet-observation resolves this directly,
	// not ViewName (ViewName is retained for descent display).
	Selector *ViewSelector `json:"selector,omitempty"`
	// Expected are the Facet checks a facet-observation Baseline evaluates
	// per targeted Entity.
	Expected []FacetExpectation `json:"expected,omitempty"`
	// RequiredRelations are outgoing relation types each targeted Entity must
	// carry ≥1 of (ADR-0085): the topology sibling of Expected — "a missing edge
	// is drift", the analog of "a missing Facet is drift". A tool-blind presence
	// predicate (the type strings are CaC, never core semantics); it opens the
	// same Findings through the same §4.3 damping. A facet-observation Baseline
	// may set expected, requiredRelations, or both. Presence-only by design —
	// never a cardinality/predicate/expression grammar (the §9 no-new-language
	// line, ADR-0085 scope boundary).
	RequiredRelations []string `json:"requiredRelations,omitempty"`
	// Claim records how the observed namespace is claimed (exclusive|
	// additive) — informational on the row; the conflict check runs at
	// compile (anti-GPO, §2.4).
	Claim string `json:"claim,omitempty"`
	// CompiledFrom marks a compiler-owned Baseline and its origin. Nil for
	// hand-written Baselines — the compiler touches only its own rows.
	CompiledFrom *CompiledOrigin `json:"compiledFrom,omitempty"`
}

// CompiledOrigin is the §1.8 descent linkage from a compiled Baseline back
// to the Intent-layer documents that produced it (ADR-0023).
type CompiledOrigin struct {
	Assignment       string `json:"assignment"`
	Intent           string `json:"intent"`
	Blueprint        string `json:"blueprint"`
	BlueprintVersion int    `json:"blueprintVersion"`
	Route            int    `json:"route"`
}

// FacetObservation is the Baseline mode for compiler-emitted, graph-side
// checks.
const FacetObservation = "facet-observation"
