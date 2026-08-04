// Package emitters is the event ingest surface (charter §2.2 Emitter,
// ADR-0018): POST /emitters/{name} authenticates machine callers by a
// token whose declaration holds only its sha256 (§2.5) and publishes
// EmitterEvents for the Trigger engine. Mounted outside /api/v1 — callers
// are alert sources, not Principals.
package emitters

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/dstout-devops/stratt/core/internal/events"
	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/macverify"
	"github.com/dstout-devops/stratt/types"
)

// TokenHeader is the DEFAULT header a caller presents its token in. An Emitter may declare
// another (ADR-0164 D1); this is the value that field falls back to.
const TokenHeader = types.DefaultTokenHeader

// emitterSource and eventSink are the two things ingest needs from the substrate, named as
// interfaces so the DOOR ITSELF is testable — *graph.Store and *events.Bus satisfy them.
//
// This seam exists because the handler had no test at all, which is how the token header came to
// be a constant nobody questioned (ADR-0164 D1). A door that authenticates untrusted callers is a
// poor place for the only coverage to be at one layer down.
type emitterSource interface {
	GetEmitter(ctx context.Context, name string) (types.Emitter, error)
}

type eventSink interface {
	PublishEmitterEvent(ctx context.Context, ev types.EmitterEvent) error
}

// Ingest serves emitter webhooks.
type Ingest struct {
	store emitterSource
	bus   eventSink
	log   *slog.Logger
	// Verifier answers "does this signature match these bytes?" for Emitters that declare a
	// signed source (ADR-0164 D2). Nil ⇒ no provider is bound, and a signed Emitter REFUSES
	// rather than degrading to unverified — an estate that declared a signature and got none
	// checked would be the worst of both.
	Verifier macverify.Verifier
}

// New builds the ingest handler set.
func New(store *graph.Store, bus *events.Bus, log *slog.Logger) *Ingest {
	return &Ingest{store: store, bus: bus, log: log.With("component", "emitters")}
}

// WithVerifier binds the MAC provider for Emitters that declare a signed source (ADR-0164 D2).
// A nil verifier is not "no verification" — it means a signed Emitter refuses, which is what an
// estate that declared a signature and got none checked deserves to see.
func (in *Ingest) WithVerifier(v macverify.Verifier) *Ingest {
	in.Verifier = v
	return in
}

// Handler serves POST /emitters/{name}.
func (in *Ingest) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.Trim(strings.TrimPrefix(r.URL.Path, "/emitters/"), "/")
		if name == "" || strings.Contains(name, "/") {
			http.Error(w, "emitter name required", http.StatusBadRequest)
			return
		}
		em, err := in.store.GetEmitter(r.Context(), name)
		if err != nil {
			// Unknown emitters 401 like bad tokens: the ingest surface
			// does not enumerate declarations for unauthenticated callers.
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if em.Kind == types.EmitterStream {
			// A stream Emitter is outbound-subscribed (ADR-0039) — it publishes
			// onto the emitter stream itself; nothing POSTs to it.
			http.Error(w, "emitter "+name+" is a stream subscriber, not an ingest endpoint", http.StatusBadRequest)
			return
		}
		// The header and prefix are the Emitter's to declare (ADR-0164 D1). GitLab presents its
		// secret as X-Gitlab-Token, others under their own names — all the same shared-token
		// model we already had, unreachable only because core insisted on the header name.
		// Nothing about the trust model moves: still hex(sha256(token)), still constant-time.
		// A declared prefix is REQUIRED, not merely tolerated. TrimPrefix would accept a bare
		// token too, which widens what is accepted past what the declaration says — the same
		// property that makes a declared header REPLACE the default rather than add to it.
		token, ok := strings.CutPrefix(r.Header.Get(em.TokenHeader()), em.TokenPrefix())
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		sum := sha256.Sum256([]byte(token))
		if token == "" || subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(strings.ToLower(em.TokenHash))) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}

		// ── The signature is checked on the RAW BYTES, before anything parses them (D3) ──
		//
		// A MAC covers exactly what the source sent. Verifying a re-serialized payload would
		// fail on whitespace, key order and number formatting — INTERMITTENTLY, which is worse
		// than failing. Nothing may touch `body` between the socket and here.
		if em.Verify != nil {
			if !in.verifySignature(r, em, body) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}

		evs, err := explode(em, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for _, ev := range evs {
			if err := in.bus.PublishEmitterEvent(r.Context(), ev); err != nil {
				in.log.Error("emitter publish failed", "emitter", name, "error", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
		}
		in.log.Info("emitter events ingested", "emitter", name, "kind", em.Kind, "events", len(evs))
		w.WriteHeader(http.StatusAccepted)
	})
}

// verifySignature answers whether this request carries a valid MAC over its own body.
//
// It logs the two failures APART, because they are different events for an operator (§1.8): a bad
// signature is one caller being refused, while an unreachable provider is EVERY signed caller being
// refused until somebody acts. The HTTP response says the same thing for both — a caller who could
// tell them apart learns something about the key.
func (in *Ingest) verifySignature(r *http.Request, em types.Emitter, body []byte) bool {
	if in.Verifier == nil {
		in.log.Error("emitter declares a signed source but no MAC verifier is bound; refusing",
			"emitter", em.Name, "keyRef", em.Verify.KeyRef)
		return false
	}
	sig, err := macverify.DecodeSignature(*em.Verify, r.Header.Get(em.Verify.Header))
	if err != nil {
		in.log.Info("emitter signature malformed", "emitter", em.Name, "error", err)
		return false
	}
	ok, err := in.Verifier.Verify(r.Context(), *em.Verify, body, sig)
	if err != nil {
		in.log.Error("emitter signature could not be verified; refusing until the provider answers",
			"emitter", em.Name, "keyRef", em.Verify.KeyRef, "error", err)
		return false
	}
	if !ok {
		in.log.Info("emitter signature rejected", "emitter", em.Name)
	}
	return ok
}

// explode turns one POST into events: webhook = the body as one payload;
// a declared `explode` = one event per entry of the named array, with the named envelope
// fields folded into each (ADR-0163 D1).
//
// A WALK OVER A DECLARATION, NOT A SWITCH ON A VENDOR'S NAME. What this replaced held
// Alertmanager's own field names — receiver, groupLabels, commonAnnotations, alerts —
// compiled into the control plane, so a source that nested its items anywhere else was a
// core change in Go (§1.4). Core's remaining knowledge of any source is that JSON has
// arrays and objects.
func explode(em types.Emitter, body []byte) ([]types.EmitterEvent, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	if em.Explode == nil {
		return []types.EmitterEvent{{Emitter: em.Name, ReceivedAt: now, Payload: envelope}}, nil
	}

	// A path that addresses nothing is REFUSED, never quietly treated as one event (§1.8).
	// Silently degrading to the un-exploded shape is the failure mode that looks like it
	// worked: every rule stops matching, nothing launches, and no surface says why.
	raw, ok := lookup(envelope, em.Explode.Path)
	if !ok {
		return nil, fmt.Errorf("explode: %s addresses nothing in this payload", em.Explode.Path)
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("explode: %s is %T, not an array — there is nothing to fan out",
			em.Explode.Path, raw)
	}

	out := make([]types.EmitterEvent, 0, len(items))
	for i, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("explode: %s[%d] is %T, not an object — an event is a set of fields",
				em.Explode.Path, i, item)
		}
		payload := make(map[string]any, len(obj)+len(em.Explode.Merge))
		maps.Copy(payload, obj)
		for _, m := range em.Explode.Merge {
			v, found := lookup(envelope, m.Path)
			if !found {
				continue // an envelope field this POST did not carry is absent, not an error
			}
			key := m.Key()
			if _, clash := payload[key]; clash {
				// No winner is invented (§2.4). Alertmanager's envelope has `status` and so
				// does every alert inside it, so this is reachable on a first POST rather
				// than theoretical — and `as:` is the estate's answer.
				return nil, fmt.Errorf(
					"explode: merging %s would overwrite %q, which %s[%d] already carries — "+
						"declare `as:` to keep them apart", m.Path, key, em.Explode.Path, i)
			}
			payload[key] = v
		}
		out = append(out, types.EmitterEvent{Emitter: em.Name, ReceivedAt: now, Payload: payload})
	}
	return out, nil
}

// lookup walks a dotted path. Explicit field lookup, nothing evaluated — ADR-0024's grammar,
// which is what keeps a declaration from becoming a language (ADR-0163 D5).
func lookup(doc map[string]any, path string) (any, bool) {
	var cur any = doc
	for _, seg := range strings.Split(path, ".") {
		if seg == "" {
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[seg]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// Explode is exported for tests.
func Explode(ctx context.Context, em types.Emitter, body []byte) ([]types.EmitterEvent, error) {
	_ = ctx
	return explode(em, body)
}
