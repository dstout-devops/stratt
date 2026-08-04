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
	// Token says WHERE the caller presents its shared token (ADR-0164 D1). Nil ⇒ the
	// default header, which is every Emitter that shipped before this field existed.
	Token *TokenSpec `json:"token,omitempty"`
	// Verify declares that this source SIGNS its body (ADR-0164 D2). Nil ⇒ no signature is
	// expected, and the ingest path stays plugin-free — the hop exists only where a
	// signature does.
	Verify *VerifySpec `json:"verify,omitempty"`
}

// Signature encodings a source may use for its MAC.
const (
	SignatureHex    = "hex"
	SignatureBase64 = "base64"
)

// VerifySpec declares how a source signs its request body.
//
// EVERY FIELD IS DATA, and a signature scheme is exactly the kind of thing that can be: a
// closed set of choices — which header, which MAC, which encoding, what prefix — describable
// without an expression language (§1). What it is NOT is a place to put a template.
//
// KeyRef is a COORDINATE and never material (§2.5). The core cannot verify a MAC itself and
// does not try: it hands the bytes and the coordinate to a provider that holds the key
// (ADR-0164 D2), the way ADR-0100 hands a DEK to a KMS rather than holding the KEK.
type VerifySpec struct {
	// Header the signature arrives in, e.g. X-Hub-Signature-256. Required.
	Header string `json:"header"`
	// Algorithm is the declared MAC, e.g. hmac-sha256. Required — inferring it from the
	// header name would be core learning one vendor's spelling again (§1.4).
	Algorithm string `json:"algorithm"`
	// Encoding of the signature value: hex (default) or base64.
	Encoding string `json:"encoding,omitempty"`
	// Prefix stripped before decoding, e.g. "sha256=". Required when declared, never merely
	// trimmed — see DecodeSignature.
	Prefix string `json:"prefix,omitempty"`
	// KeyRef names the verification key by coordinate, for the provider to resolve.
	KeyRef string `json:"keyRef"`

	// ── ADR-0167 · a replay is a valid signature at the wrong time ─────────────────────────
	//
	// Format is how the header is SHAPED: raw (the whole value is the signature) or kv
	// ("t=…,v1=…", which is how Stripe and Slack carry a timestamp beside the MAC).
	Format string `json:"format,omitempty"`
	// SignatureKey and TimestampKey name the pairs, for kv only.
	SignatureKey string `json:"signatureKey,omitempty"`
	TimestampKey string `json:"timestampKey,omitempty"`
	// SignedPayload is what the MAC covers. An ENUM, never a template: "{timestamp}.{body}"
	// would be a two-token templating language, and then somebody wants {header:X} and a
	// separator and an ordering, and the estate has an expression evaluator nobody decided
	// to build (§1).
	SignedPayload string `json:"signedPayload,omitempty"`
	// ToleranceSeconds bounds how old a signed timestamp may be. Declared, because only the
	// operator knows their clock skew and their source's retry behaviour — there is no safe
	// default to pick on their behalf.
	ToleranceSeconds int `json:"toleranceSeconds,omitempty"`
}

// Header formats and signed-payload shapes (ADR-0167 D1). Closed sets on purpose.
const (
	SignatureFormatRaw = "raw"
	SignatureFormatKV  = "kv"

	SignedPayloadBody          = "body"
	SignedPayloadTimestampBody = "timestamp.body"
)

// DefaultTokenHeader is where a caller presents its token when the Emitter declares nothing —
// the header Stratt has always named, now the default of a declared field rather than the only
// possibility (ADR-0164 D1).
const DefaultTokenHeader = "X-Stratt-Emitter-Token"

// TokenSpec declares where a source presents its shared token.
//
// NOTHING ABOUT THE TRUST MODEL CHANGES HERE: the declaration and the database still hold only
// hex(sha256(token)) (§2.5), and the comparison is still constant-time. A source that sends its
// secret under its own header name — GitLab's X-Gitlab-Token, and a long tail of others — was
// unreachable for the sole reason that core insisted on the name.
type TokenSpec struct {
	// Header the token arrives in. Empty ⇒ DefaultTokenHeader.
	Header string `json:"header,omitempty"`
	// Prefix stripped before comparison, e.g. "Bearer ". Empty ⇒ the whole value is the token.
	Prefix string `json:"prefix,omitempty"`
}

// TokenHeader is the header this Emitter's callers use.
func (e Emitter) TokenHeader() string {
	if e.Token != nil && e.Token.Header != "" {
		return e.Token.Header
	}
	return DefaultTokenHeader
}

// TokenPrefix is stripped from the presented value before comparison.
func (e Emitter) TokenPrefix() string {
	if e.Token == nil {
		return ""
	}
	return e.Token.Prefix
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
