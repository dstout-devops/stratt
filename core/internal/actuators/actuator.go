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

	"github.com/dstout-devops/stratt/types"
)

// Target is one Entity rendered as an execution target.
type Target struct {
	EntityID string
	// Name is the target alias used in tool content and per-target results.
	Name string
	// Vars are tool-level connection/host vars (e.g. ansible_connection).
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
