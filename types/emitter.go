package types

// Emitter kinds (charter §2.2: the Connector capability that turns external
// happenings into events). Two, and deliberately no more: how a POST is SHAPED
// is a declaration, not a kind (ADR-0163 D2).
const (
	EmitterWebhook = "webhook"
	// EmitterStream is a poller/stream-subscriber Emitter (charter §2.2): it
	// outbound-connects to an external stream (e.g. the Salt event bus, ADR-0039)
	// and publishes onto the emitter stream. It has NO inbound token (nothing
	// POSTs to it), so TokenHash is empty. Registering one claims its name in the
	// registry, so a token-authed webhook Emitter can't collide with it.
	EmitterStream = "stream"

	// EmitterAlertmanager is RETIRED as a kind (ADR-0163 D2) and survives only as the
	// spelling old declarations use: the parser rewrites it into the equivalent Explode
	// block. It was a vendor's product name in core's enum AND in the published OpenAPI
	// contract, which charter §2 bans in a core identifier and §1.4 bans in the spine.
	// Delete this constant and its normalization together, once no declaration says it.
	EmitterAlertmanager = "alertmanager"
)

// Emitter is a CaC-declared event ingest point (ADR-0018). TokenHash is
// sha256 over the caller's bearer token — the declaration and the database
// hold only the hash (§2.5: nothing secret in Git; nothing to leak from the
// registry). Callers present the raw token in X-Stratt-Emitter-Token.
type Emitter struct {
	Name string `json:"name"`
	// Kind is webhook | stream.
	Kind string `json:"kind"`
	// TokenHash is hex(sha256(token)) for receive kinds; EMPTY for a stream
	// subscriber (no inbound token).
	TokenHash string `json:"tokenHash"`
	// Explode fans ONE POST into many events (ADR-0163 D1). Nil ⇒ one POST is one
	// event, which is every Emitter that shipped before this field existed.
	Explode *ExplodeSpec `json:"explode,omitempty"`
}

// ExplodeSpec declares how a source's payload carries several happenings — as DATA, because
// core knowing any particular tool's field names is the §1.4 line this removes.
//
// Nothing here evaluates: Path and MergeKey.Path are dotted lookups, the same binding
// ADR-0024 blessed and ADR-0161/0162 reused. An expression language would answer this and
// every future variant of it, and would make "what does event.x mean here?" a computation
// rather than something a reader can see in the declaration (§1, ADR-0163 D5).
type ExplodeSpec struct {
	// Path addresses the ARRAY to fan out: one event per entry. Required.
	Path string `json:"path"`
	// Merge names envelope fields folded into every exploded event. Explicit, never
	// "everything except the array" — an implicit merge would let the source adding one
	// top-level field silently change the payload every rule matches against (D3).
	Merge []MergeKey `json:"merge,omitempty"`
}

// MergeKey is one envelope field folded into each exploded event.
type MergeKey struct {
	// Path is the dotted lookup into the envelope.
	Path string `json:"path"`
	// As renames it in the event payload. The reason this exists is concrete: an
	// Alertmanager envelope has `status` and so does every alert inside it, so the obvious
	// declaration collides on the first POST. A collision is REFUSED rather than resolved
	// (§2.4 — no implicit precedence), and this is how the estate resolves it.
	As string `json:"as,omitempty"`
}

// Key is the name this merged field takes in the event payload.
func (m MergeKey) Key() string {
	if m.As != "" {
		return m.As
	}
	if i := lastDot(m.Path); i >= 0 {
		return m.Path[i+1:]
	}
	return m.Path
}

func lastDot(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}

// EmitterEvent is one ingested event on the emitter stream: what Trigger
// rules evaluate against.
type EmitterEvent struct {
	Emitter    string         `json:"emitter"`
	ReceivedAt string         `json:"receivedAt"`
	Payload    map[string]any `json:"payload"`
}
