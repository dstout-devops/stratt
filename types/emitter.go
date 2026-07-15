package types

// Emitter kinds (charter §2.2: the Connector capability that turns external
// happenings into events). v1 ships webhook (generic JSON POST) and
// alertmanager (Alertmanager webhook payloads, exploded per alert).
const (
	EmitterWebhook      = "webhook"
	EmitterAlertmanager = "alertmanager"
	// EmitterStream is a poller/stream-subscriber Emitter (charter §2.2): it
	// outbound-connects to an external stream (e.g. the Salt event bus, ADR-0039)
	// and publishes onto the emitter stream. It has NO inbound token (nothing
	// POSTs to it), so TokenHash is empty. Registering one claims its name in the
	// registry, so a token-authed webhook Emitter can't collide with it.
	EmitterStream = "stream"
)

// Emitter is a CaC-declared event ingest point (ADR-0018). TokenHash is
// sha256 over the caller's bearer token — the declaration and the database
// hold only the hash (§2.5: nothing secret in Git; nothing to leak from the
// registry). Callers present the raw token in X-Stratt-Emitter-Token.
type Emitter struct {
	Name string `json:"name"`
	// Kind is webhook | alertmanager | stream.
	Kind string `json:"kind"`
	// TokenHash is hex(sha256(token)) for receive kinds; EMPTY for a stream
	// subscriber (no inbound token).
	TokenHash string `json:"tokenHash"`
}

// EmitterEvent is one ingested event on the emitter stream: what Trigger
// rules evaluate against.
type EmitterEvent struct {
	Emitter    string         `json:"emitter"`
	ReceivedAt string         `json:"receivedAt"`
	Payload    map[string]any `json:"payload"`
}
