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
	"github.com/dstout-devops/stratt/types"
)

// Base JetStream stream / KV names and subject roots (mirrors events.go's
// STRATT_* naming). A named Cell scopes every one of them so peer Cells sharing
// a NATS cluster never cross-wire each other's dispatch plane (ADR-0044 slice
// 6); the built-in LocalCell (scope "") keeps them byte-identical.
const (
	baseDispatchStream   = "STRATT_DISPATCH"
	baseResultStream     = "STRATT_DISPATCH_RESULT"
	baseLivenessBucket   = "SITE_LIVENESS"
	baseDispatchSubjRoot = "stratt.dispatch"
	baseResultSubjRoot   = "stratt.dispatchresult"
)

// The Cell-scoped names, derived once by SetScope at boot. They are package
// vars (mirroring orchestrate.TaskQueue) because siteproto's pure subject
// functions are shared verbatim by the hub (strattd) and the agent
// (stratt-agent): both call SetScope with the SAME token so they always agree.
var (
	// DispatchStream is a work-queue stream of hub→Site dispatch requests;
	// per-Site durable pull consumers drain it (store-and-forward + redelivery).
	DispatchStream = baseDispatchStream
	// ResultStream carries Site→hub terminal results, correlated by (runID, slice).
	ResultStream = baseResultStream
	// LivenessBucket is a TTL'd KV of agent heartbeats so the hub fails a
	// dead-Site branch fast instead of eating activity timeouts.
	LivenessBucket = baseLivenessBucket
	// DispatchSubjectPrefix roots every hub→Site dispatch subject.
	DispatchSubjectPrefix = baseDispatchSubjRoot
	// resultSubjectPrefix roots every Site→hub result subject.
	resultSubjectPrefix = baseResultSubjRoot
	// DispatchStreamSubjects is what the work-queue stream binds: every per-Site
	// dispatch subject, but NOT the 4-token cancel subjects (those are ephemeral
	// core-NATS signals, never queued work).
	DispatchStreamSubjects = baseDispatchSubjRoot + ".*"
	// ResultStreamSubjects is what the result stream binds.
	ResultStreamSubjects = baseResultSubjRoot + ".>"
)

// SetScope Cell-scopes every stream/KV name and subject root for this daemon's
// Cell (ADR-0044 slice 6). Both strattd and stratt-agent MUST call it once at
// boot with types.CellScopeToken(cellID, override) — the identical token — or
// hub and agent publish/subscribe on different subjects and silently talk past
// each other. scope "" (LocalCell) is a no-op: every name stays byte-identical
// to the pre-Cells control plane.
func SetScope(scope string) {
	DispatchStream = types.ScopedStream(baseDispatchStream, scope)
	ResultStream = types.ScopedStream(baseResultStream, scope)
	LivenessBucket = types.ScopedStream(baseLivenessBucket, scope)
	// ScopedSubjectRoot inserts the token as the second subject token
	// ("stratt.dispatch" → "stratt.<tok>.dispatch"), so the bare roots scope
	// without any trailing-dot juggling.
	DispatchSubjectPrefix = types.ScopedSubjectRoot(baseDispatchSubjRoot, scope)
	resultSubjectPrefix = types.ScopedSubjectRoot(baseResultSubjRoot, scope)
	DispatchStreamSubjects = DispatchSubjectPrefix + ".*"
	ResultStreamSubjects = resultSubjectPrefix + ".>"
}

// DispatchSubject is the work subject for one Site (3 tokens under the root).
func DispatchSubject(site string) string { return DispatchSubjectPrefix + "." + site }

// CancelSubject is the ephemeral hub→Site cancellation signal for a Run (4
// tokens, so it never matches DispatchStreamSubjects).
func CancelSubject(site string) string { return DispatchSubjectPrefix + ".cancel." + site }

// ResultSubject correlates a terminal result to its dispatched slice.
func ResultSubject(runID string, slice int) string {
	return fmt.Sprintf("%s.%s.%d", resultSubjectPrefix, runID, slice)
}

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
