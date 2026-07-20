// Package actuators is the in-tree Actuator seam (charter §2.3): an Actuator
// prepares tool content for a K8s Job pod and interprets the pod's event
// stream back into the platform's task-event shape.
//
// This Go interface is core-trust-tier execution plumbing, not the plugin
// Contract. Contracts stay pinned, hash-verified JSON Schema documents (§1.5)
// and land with the Phase-2 Contract machinery; out-of-tree Actuators speak
// those Contracts over the plugin transports, never this interface.
package actuators

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/dstout-devops/stratt/types"
)

// Target is one Entity rendered as an execution target.
type Target struct {
	EntityID string
	// Name is the target alias used in tool content and per-target results.
	Name string
	// Address is the typed management reachability coordinate the core resolved
	// from the Entity's mgmt.address Facet (ADR-0084). It is a FIRST-CLASS field,
	// NOT a tool var: the core never authors a connection key (no ansible_host in
	// the spine, §1.4) — a connection Actuator renders its own var FROM this.
	// Empty ⇒ the target declared no reachability (unroutable, never silent-local).
	Address string
	// Vars are genuinely tool-authored vars only — never a core-emitted connection
	// key. The reachability coordinate is Address above, not a var (ADR-0084 §1.4).
	Vars map[string]string
}

// JobSpec is everything a prepared Step needs from the dispatcher. The
// dispatcher stays tool-agnostic: it mounts Files, runs Command, and streams
// stdout lines back through Interpret.
type JobSpec struct {
	// Files are mounted read-only into the pod at /runner/<key> — keys are
	// relative paths (e.g. "project/play.yml", "inventory/hosts").
	Files map[string]string
	// Command is the container command.
	Command []string
	// Image overrides the dispatcher's default EE image when non-empty.
	Image string
	// Env is actuator-computed plain environment for the pod (e.g. the
	// state-backend credential, ADR-0016). CredentialRef material never
	// travels here — that stays on the secretKeyRef path (§2.5).
	//
	// CAUTION (Sites, ADR-0032): because Env carries plain values that MAY be
	// material (the opentofu TF_HTTP_PASSWORD is one), a JobSpec with non-empty
	// Env is NOT safe to serialize onto NATS or pack into a signed Bundle — see
	// RemoteSafe. Env-populating actuators (opentofu, ansible-SCM) stay
	// hub-local in v1.
	Env map[string]string
}

// RemoteSafe reports whether this JobSpec may leave the hub process — be
// serialized into a NATS dispatch to a remote Site or packed into a
// cosign-signed Bundle (ADR-0032). A JobSpec is remote-safe only when it
// carries no plain Env: Env may hold credential material (e.g. the opentofu
// state-backend password, ADR-0016), and once serialized that material crosses
// the wire or lands durably in a distributable artifact — a §2.5 violation.
// The gate is structural, not a review norm, and deliberately conservative
// (any non-empty Env is refused, even non-secret keys) until Env separates
// material from plain config. Never include a key's VALUE in the error — that
// is exactly the material we refuse to surface.
func (s JobSpec) RemoteSafe() error {
	if len(s.Env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return fmt.Errorf("job spec is not remote-safe: it sets pod env %v, which may carry credential material and must not cross a Site boundary or enter a Bundle (§2.5); this actuator stays hub-local in v1", keys)
}

// Per-target statuses. "changed" implies ok; failed and unreachable are both
// failures for the Run-success fold but stay distinct for diagnosis (§1.8).
const (
	StatusOK          = "ok"
	StatusChanged     = "changed"
	StatusFailed      = "failed"
	StatusUnreachable = "unreachable"
)

// TargetResult is a terminal per-target outcome.
type TargetResult struct {
	Target string
	// Status is one of the Status* values.
	Status string
	// Failed is the seam's success fold: true for failed and unreachable.
	Failed bool
}

// EntityObservation is one Entity a tool's output declares into existence —
// projected with Run provenance by the orchestration layer (ADR-0017,
// charter §4 provision→configure). Identity and labels only: Facets arrive
// from later Steps or Syncers (§1.1).
type EntityObservation struct {
	Kind         string
	IdentityKeys map[string]string
	Labels       map[string]string
	// Relations the observation carries — a build's placed-in edge (ADR-0059), each
	// targeting a resolved Entity BY IDENTITY. Projected Run-provenance alongside the
	// entity (ProjectFacts), so a build projects its topology, not just identity.
	Relations []RelationObservation
}

// RelationObservation is a write-back edge to a target named by identity (the target
// Entity is resolved at projection; an unresolved target drops the edge).
type RelationObservation struct {
	Type     string
	ToScheme string
	ToValue  string
}

// Interpreted is one understood line of pod stdout.
type Interpreted struct {
	// Event is the task event to publish (RunID is stamped by the
	// dispatcher). Seq must be deterministic per Run so retry re-publishes
	// dedup server-side (events.Publish MsgID).
	Event types.RunEvent
	// Result is non-nil when this event is a terminal per-target outcome.
	Result *TargetResult
	// Facts are Facet-namespace → value fragments carried by this event for
	// Event.Target, to project back with Run provenance (§8). Nil when the
	// event carries none.
	Facts map[string]json.RawMessage
	// Entities are tool-declared Entity observations carried by this event
	// (e.g. the opentofu stratt_entities output).
	Entities []EntityObservation
	// OutputsContract is a tool-derived (rung-2) schema document for the
	// Step's outputs, when the event carries one (§2.2).
	OutputsContract json.RawMessage
	// Outputs are the typed output VALUES an Action produced (§2.2: an Action
	// declares an output Contract). Validated against actions/<name>.output and
	// captured on the Run for cross-Step binding (ADR-0031). Actuators leave it
	// nil — output values are the Action seam's defining feature.
	Outputs json.RawMessage
	// Drift is one observed-vs-expected fragment for Event.Target carried by
	// this event (a check-mode task diff, a planned resource change) —
	// already redacted upstream. The dispatcher accumulates fragments per
	// target, size-capped, for Baseline evaluation (ADR-0019).
	Drift json.RawMessage
}

// Actuator prepares tool content and interprets the resulting event stream
// (charter §2.3: Actuators interpret content and produce many effects).
type Actuator interface {
	// Name is the Actuator's registry name (§2 vocabulary: ansible, script,
	// opentofu, helm, mcp, …).
	Name() string
	// Prepare renders Step params and targets into a JobSpec. Params are
	// actuator-interpreted; their JSON-Schema Contract document is Phase-2.
	Prepare(params json.RawMessage, targets []Target) (JobSpec, error)
	// Interpret decodes one stdout line. Lines that are not events for this
	// Actuator (banner noise) return ok=false.
	Interpret(line []byte) (Interpreted, bool)
}
