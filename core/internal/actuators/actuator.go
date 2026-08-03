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

// Transport is a target's observed connection method (ADR-0156 D2).
//
// Kind is LEGIBLE — the core carries and logs it exactly as it carries Address, so a Run's
// descent can say which transport a target used (§1.8) — but the core NEVER BRANCHES ON IT.
// That is what keeps the spine from holding a closed set of substrates it would have to grow
// (§9). Coordinates is the validated mgmt.transport document, OPAQUE for the same reason
// `desired` is: the shape belongs to the transport, and a core that parsed it would be
// learning what a Kubernetes namespace is (§1.5).
type Transport struct {
	Kind        string
	Coordinates json.RawMessage
}

// Target is one Entity rendered as an execution target.
type Target struct {
	EntityID string
	// Name is the target alias used in tool content and per-target results.
	Name string
	// Groups are the named partitions this target belongs to, resolved by the CORE from the Step's
	// declared GroupBy keys against the graph (ADR-0161 D1). The Actuator renders them in whatever
	// its tool calls a group — INI sections, for ansible — exactly as it renders Address into its
	// own connection var: the core authors no tool syntax, the tool learns no graph vocabulary.
	//
	// Empty ⇒ this target is in no named partition, which is every Run before ADR-0161 and every
	// Step that declares no GroupBy. It NEVER means "put it somewhere else": a target with no value
	// for a key joins no group from that key rather than an invented bucket (§1.2 — the graph does
	// not carry the value, so nothing may assert one).
	Groups []string
	// Address is the typed management reachability coordinate the core resolved
	// from the Entity's mgmt.address Facet (ADR-0084). It is a FIRST-CLASS field,
	// NOT a tool var: the core never authors a connection key (no ansible_host in
	// the spine, §1.4) — a connection Actuator renders its own var FROM this.
	// Empty ⇒ the target declared no reachability (unroutable, never silent-local).
	Address string
	// Port is the optional management port paired with Address, from the same
	// mgmt.address Facet. Typed and first-class for the same reason Address is: the
	// core resolves the coordinate, the connection Actuator renders the tool's own
	// port var (ansible_port, …) FROM it. 0 ⇒ no declared port; the tool's default
	// applies — the core never invents one.
	Port int32
	// Transport is HOW this target is reached, resolved from its mgmt.transport Facet
	// (ADR-0156). Address says WHERE; this says BY WHAT MEANS, and it is the fact that
	// makes a MIXED-SUBSTRATE target set convergeable: a pod reached by kubectl, a VM by
	// vmware_tools and an EC2 instance by aws_ssm can sit in one Apply, because the
	// connection method rides the TARGET rather than the Step's config.
	//
	// Nil ⇒ nothing was observed about how to reach this target, and the Actuator applies
	// its own default. That is DISTINCT from an observed `ssh` transport, which means a
	// Syncer determined it.
	Transport *Transport
	// Vars are genuinely tool-authored vars only — never a core-emitted connection
	// key. The reachability coordinate is Address above, not a var (ADR-0084 §1.4).
	Vars map[string]string
	// Jump is the resolved reached-via chain, NEAREST HOP FIRST: the bastions this
	// target is reachable through (ADR-0126 D3). Each hop's coordinate comes from that
	// hop Entity's OWN mgmt.address, so a bastion's address has exactly one home in the
	// model and cannot drift from the graph's (§2.4) — which is why the topology is a
	// Relation and not a string field on the target's own Facet (§9: the mgmt.address
	// schema stays closed).
	//
	// Typed and first-class for the same reason Address is: the core resolves the
	// COORDINATE, the plugin renders the connection (ProxyJump, -J, whatever its
	// transport calls it). There is no ssh flag anywhere in core.
	Jump []Hop
}

// Hop is one resolved link in a reached-via chain — a bastion's reachability
// coordinate, nothing more. No credential and no user: authenticating to a hop is Step
// config (params.connection.jump), the same address-vs-credential split ADR-0084 D4 drew
// for the target itself.
type Hop struct {
	// Name is the hop Entity's observed name — carried for diagnosis, so a failure can
	// say WHICH bastion was unreachable rather than only its address (§1.8).
	Name    string
	Address string
	Port    int32
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
	// Facets the observation carries, namespace → value, ALREADY GOVERNED (grant ceiling
	// ∩ the Step's facetWriteScope, ADR-0054) by the time they reach here. Projected with
	// Run provenance against the Entity this observation upserts.
	//
	// IT EXISTS BECAUSE ONE SEAM HAD TWO FATES. `pluginhost.ApplyEntity` has always carried
	// `Facets`, and the governor has always gated them — but only the EE-Job door consumed
	// them (`executeJobPlugin`, via `res.Facts[host.name]`). The gRPC door built an
	// EntityObservation out of Kind/IdentityKeys/Labels and dropped the rest on the floor,
	// because this struct had nowhere to put it. So the same governed write-back was
	// honoured over one transport and discarded over the other, with the governor doing
	// real work — admitting namespaces, emitting Rejections for the ones it refused —
	// immediately before the survivors were thrown away.
	//
	// Nothing shipping hit it: `dns.yaml` and `awsec2.yaml` each DECLINE the
	// `facetNamespaces` grant with a comment saying it would be "authority granted for a
	// path that does not exist". That the estate had learned to route around a hole is the
	// argument for closing it, not for leaving it — a grant that reads as permitted and is
	// honoured by nothing is exactly the shape ADR-0054 warns about, and a dropped
	// write-back reports as nothing at all.
	//
	// NOT the Action path. `pluginhost.ActionEntity` still carries no Facets, deliberately
	// (ADR-0113 D3): a build projects BY IDENTITY ONLY and its Facets arrive from the
	// Syncer's next OBSERVE. Apply is the Actuator's content verb, where the fact-back
	// convention lives (ADR-0084) — which is why the EE-Job door had it all along.
	Facets map[string]json.RawMessage
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
