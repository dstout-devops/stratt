// Package siteproto is the hub↔Site wire contract (charter §2.3, ADR-0032):
// the JSON payloads and NATS subject/stream names a remote Site's stratt-agent
// exchanges with the control plane. It is deliberately thin and dependency-light
// so both the hub (strattd) and the agent (stratt-agent) import it as the single
// shared shape — the backend-go "factor shared wire types into a common module"
// rule, satisfied within the core module since the agent is a core/cmd binary.
//
// §2.5 invariant: a DispatchRequest carries credential POINTERS
// (dispatch.CredentialMount) and a RemoteSafe JobSpec only — never material.
// The gateway enforces Spec.RemoteSafe() before publish.
package siteproto

import (
	"fmt"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/core/internal/dispatch"
)

// JetStream stream / KV names (mirrors events.go's STRATT_* naming).
const (
	// DispatchStream is a work-queue stream of hub→Site dispatch requests;
	// per-Site durable pull consumers drain it (store-and-forward + redelivery).
	DispatchStream = "STRATT_DISPATCH"
	// ResultStream carries Site→hub terminal results, correlated by (runID, slice).
	ResultStream = "STRATT_DISPATCH_RESULT"
	// LivenessBucket is a TTL'd KV of agent heartbeats so the hub fails a
	// dead-Site branch fast instead of eating activity timeouts.
	LivenessBucket = "SITE_LIVENESS"
)

// DispatchSubjectPrefix roots every hub→Site dispatch subject.
const DispatchSubjectPrefix = "stratt.dispatch"

// DispatchSubject is the work subject for one Site (3 tokens).
func DispatchSubject(site string) string { return DispatchSubjectPrefix + "." + site }

// DispatchStreamSubjects is what the work-queue stream binds: every per-Site
// dispatch subject, but NOT the 4-token cancel subjects (those are ephemeral
// core-NATS signals, never queued work).
const DispatchStreamSubjects = DispatchSubjectPrefix + ".*"

// CancelSubject is the ephemeral hub→Site cancellation signal for a Run (4
// tokens, so it never matches DispatchStreamSubjects).
func CancelSubject(site string) string { return DispatchSubjectPrefix + ".cancel." + site }

// ResultSubject correlates a terminal result to its dispatched slice.
func ResultSubject(runID string, slice int) string {
	return fmt.Sprintf("stratt.dispatchresult.%s.%d", runID, slice)
}

// ResultStreamSubjects is what the result stream binds.
const ResultStreamSubjects = "stratt.dispatchresult.>"

// DispatchRequest is one hub→Site work item: a prepared, RemoteSafe JobSpec
// plus credential POINTERS to run one slice of a Run at a remote Site. The
// agent does not re-Prepare — it looks up the named Interpreter and runs Spec
// directly through the same dispatch.Dispatcher.Run the hub uses.
type DispatchRequest struct {
	RunID string `json:"runId"`
	// Slice is the GLOBAL slice index the hub allocated across all (Site, chunk)
	// pairs — it keys the Job name and the event MsgID, so it must stay unique
	// across the whole Run (ADR-0032; prevents cross-Site event dedup-erasure).
	Slice int    `json:"slice"`
	Site  string `json:"site"`
	// Actuator or Action names the in-agent Interpreter for this Spec (exactly
	// one is set). Actuator content Runs use Actuator; targetless Actions use
	// Action.
	Actuator string `json:"actuator,omitempty"`
	Action   string `json:"action,omitempty"`
	DryRun   bool   `json:"dryRun,omitempty"`
	// Spec is the prepared pod content — MUST satisfy RemoteSafe (no plain Env
	// material). Enforced by the gateway before publish (§2.5).
	Spec actuators.JobSpec `json:"spec"`
	// Creds are credential POINTERS the agent resolves against its OWN local
	// Secrets at pod spawn. Never material.
	Creds []dispatch.CredentialMount `json:"creds,omitempty"`
}

// DispatchResult is the Site→hub terminal outcome for one dispatched slice.
// The full task-event stream flows separately over the NATS leaf into the hub's
// run-events stream; this carries only the compact Result the workflow merges.
type DispatchResult struct {
	RunID  string          `json:"runId"`
	Slice  int             `json:"slice"`
	Site   string          `json:"site"`
	Result dispatch.Result `json:"result"`
	// Err is a terminal execution error the Site could not express as a
	// per-target failure (e.g. MissingCredential, a refused Bundle). Empty on a
	// normal result; non-empty fails the branch activity on the hub.
	Err string `json:"err,omitempty"`
}

// Liveness is a Site agent's ephemeral heartbeat, written TTL'd into the
// SITE_LIVENESS KV (never the graph — §1.2). The hub reads it to show live
// up/down state on GET /sites and to fail a dead-Site branch fast.
type Liveness struct {
	Site    string `json:"site"`
	Mode    string `json:"mode"`
	Version string `json:"version,omitempty"`
	// At is the agent's heartbeat timestamp (RFC3339); the KV TTL expires a
	// dead agent's key, so presence alone means "recently alive".
	At string `json:"at"`
}
