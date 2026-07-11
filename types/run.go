package types

import "time"

// RunStatus is the lifecycle state of a Run.
type RunStatus string

const (
	RunPending   RunStatus = "pending"
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
	RunCanceled  RunStatus = "canceled"
)

// Run is one execution instance: status, per-target results, event stream,
// artifacts, cost/usage, and the provenance it wrote (charter §2.3).
// Postgres stores summaries only; the event stream lives on NATS and
// artifacts in object storage (§3 — the AWX job-events-table pathology,
// eliminated).
type Run struct {
	ID string `json:"id"`
	// WorkflowID is the Temporal workflow execution this Run belongs to.
	WorkflowID string    `json:"workflowId"`
	Status     RunStatus `json:"status"`
	// ViewRef and ViewVersion record exactly which View (at which version)
	// the Run targeted — the blast-radius audit trail (§4.3).
	ViewRef     string     `json:"viewRef,omitempty"`
	ViewVersion int64      `json:"viewVersion,omitempty"`
	StartedAt   time.Time  `json:"startedAt"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
}

// RunEvent is one task event in a Run's stream — the floor of the §1.8
// descent ladder (Intent → Blueprint route → Workflow → Run → task event).
// Events stream over NATS; only summaries persist in Postgres.
type RunEvent struct {
	RunID string `json:"runId"`
	// Slice is the target-set slice this event came from (0 when the Run is
	// unsliced). (RunID, Slice, Seq) is the event's identity: Seq is only
	// unique within one slice's tool stream.
	Slice int       `json:"slice,omitempty"`
	Seq   int64     `json:"seq"`
	At    time.Time `json:"at"`
	// Kind is the event type (e.g. "task-start", "task-ok", "task-failed",
	// "stdout").
	Kind string `json:"kind"`
	// Target is the Entity the event applies to, when per-target.
	Target string `json:"target,omitempty"`
	// Payload is the event body (tool-shaped, opaque to the spine).
	Payload map[string]any `json:"payload,omitempty"`
}
