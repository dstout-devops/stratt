package types

import "time"

// Step edge conditions: when a Step becomes eligible relative to its needs
// (charter §2: success/failure/always edges). Empty means success.
const (
	WhenSuccess = "success"
	WhenFailure = "failure"
	WhenAlways  = "always"
)

// Workflow is a Temporal-backed DAG of Steps (charter §2). v1 ships needs-
// edges with success/failure/always conditions and human Gates; cross-Step
// output binding (Contracts), nesting, convergence, and policy Gates are the
// Phase-2 extensions (ADR-0011).
type Workflow struct {
	Name  string `json:"name"`
	Steps []Step `json:"steps"`
}

// Step is one node of the DAG (§2.3): an actuation (Actuator + params against a
// View), an Action (a targetless typed operation — Action + params, ADR-0031),
// or a Gate. Exactly one of the three shapes is set.
type Step struct {
	Name string `json:"name"`
	// Needs lists Step names that must reach a terminal state first.
	Needs []string `json:"needs,omitempty"`
	// When gates eligibility on the needs' outcomes: success (default,
	// all needs succeeded), failure (≥1 need failed), always.
	When string `json:"when,omitempty"`

	// Gate makes this a human-approval Step (§2: Gates).
	Gate *GateSpec `json:"gate,omitempty"`

	// Actuation fields (mirror StartRun / Trigger launch parameters).
	ViewName string `json:"viewName,omitempty"`
	Actuator string `json:"actuator,omitempty"`
	// Action names a Connector Action for a targetless typed operation (§2.2,
	// ADR-0031); mutually exclusive with ViewName/Actuator. DryRun asks for a
	// side-effect-free plan.
	Action         string         `json:"action,omitempty"`
	DryRun         bool           `json:"dryRun,omitempty"`
	Params         map[string]any `json:"params,omitempty"`
	Slices         int            `json:"slices,omitempty"`
	CredentialRefs []string       `json:"credentialRefs,omitempty"`
	// FacetWriteScope is the Facet namespaces an actuation Step may write back
	// (ADR-0054): the actuator's grant ∩ this scope. Empty admits no facet write-back.
	FacetWriteScope []string `json:"facetWriteScope,omitempty"`

	// Plan marks an actuation Step that runs the Actuator's PLAN verb — the
	// canonical producer of a hash-pinned saved plan (ADR-0047 §7/§8). It outputs
	// the plan digest ({{.steps.<name>.outputs.planDigest}}) that a downstream Gate
	// binds and a plan-pinned Apply consumes. (A DryRun streaming Apply is
	// diagnostic and is NOT pinnable — only a Plan step produces the pin.)
	Plan bool `json:"plan,omitempty"`
	// PlanFrom names the Plan Step whose digest this Step consumes — on a Gate it
	// is the digest bound into the Gate record (approve-what-you-see, §1.8); on an
	// Apply it is the plan applied EXACTLY. A plan-pinned Apply MUST be transitively
	// guarded by a Gate with the SAME PlanFrom (validated at load, fail-closed —
	// ADR-0047 §8): the core never silently degrades a missing pin to an unpinned
	// live apply of `desired`.
	PlanFrom string `json:"planFrom,omitempty"`
}

// GateSpec declares who may decide a Gate and how long it waits.
type GateSpec struct {
	Approvers GateApprovers `json:"approvers"`
	// TimeoutSeconds expires the Gate (recorded as expired, treated as
	// denial) after this long pending; 0 waits forever.
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
}

// GateApprovers authorizes a decision: the Principal is listed explicitly or
// is a member of one of the teams (checked through the Authorizer seam —
// same answer from the tuple evaluator and OpenFGA).
type GateApprovers struct {
	Principals []string `json:"principals,omitempty"`
	Teams      []string `json:"teams,omitempty"`
}

// Gate statuses.
const (
	GatePending  = "pending"
	GateApproved = "approved"
	GateDenied   = "denied"
	GateExpired  = "expired"
)

// Gate is one pending-or-decided approval instance of a Gate Step within a
// WorkflowRun. The deciding Principal and note are the audit trail (§1.6).
type Gate struct {
	ID            string        `json:"id"`
	WorkflowRunID string        `json:"workflowRunId"`
	Step          string        `json:"step"`
	Status        string        `json:"status"`
	Approvers     GateApprovers `json:"approvers"`
	DecidedBy     string        `json:"decidedBy,omitempty"`
	Note          string        `json:"note,omitempty"`
	CreatedAt     time.Time     `json:"createdAt"`
	DecidedAt     *time.Time    `json:"decidedAt,omitempty"`
	// PlanDigest is the exact saved-plan sha256 this Gate approves (ADR-0047 §8) —
	// WRITE-ONCE at creation (a re-plan never silently rebinds), the digest the
	// approver sees (approve-what-you-see, §1.8), and the value the plan-pinned
	// Apply is verified against at its boundary. Empty for a non-plan Gate.
	PlanDigest string `json:"planDigest,omitempty"`
}

// WorkflowRun is one execution of a Workflow — the rung between Workflow and
// its per-Step Runs on the §1.8 descent ladder.
type WorkflowRun struct {
	ID           string `json:"id"`
	WorkflowName string `json:"workflowName"`
	// TemporalID is the Temporal workflow execution driving the DAG.
	TemporalID string    `json:"temporalId,omitempty"`
	Status     RunStatus `json:"status"`
	// Principal is the launching identity; every Step's credential use
	// check runs against it (§2.5).
	Principal string `json:"principal,omitempty"`
	// TriggeredBy names the Trigger that fired this execution; empty for
	// API launches (§1.8 descent: Trigger → WorkflowRun).
	TriggeredBy string     `json:"triggeredBy,omitempty"`
	StartedAt   time.Time  `json:"startedAt"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	// Cell is the control-plane Cell whose Temporal owns this execution (ADR-0044
	// slice 5) — set once at creation. A Gate decision or cancel routes here.
	// Empty ⇒ the built-in LocalCell.
	Cell string `json:"cell,omitempty"`
}
