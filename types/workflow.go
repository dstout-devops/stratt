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

// Step is one node of the DAG: either an actuation (one contracted
// invocation — Actuator + params against a View, §2.3) or a Gate. Exactly
// one of the two shapes is set.
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
	ViewName       string         `json:"viewName,omitempty"`
	Actuator       string         `json:"actuator,omitempty"`
	Params         map[string]any `json:"params,omitempty"`
	Slices         int            `json:"slices,omitempty"`
	CredentialRefs []string       `json:"credentialRefs,omitempty"`
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
}
