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
	"github.com/dstout-devops/stratt/types"
)

// TokenHeader carries the caller's raw token.
const TokenHeader = "X-Stratt-Emitter-Token"

// Ingest serves emitter webhooks.
type Ingest struct {
	store *graph.Store
	bus   *events.Bus
	log   *slog.Logger
}

// New builds the ingest handler set.
func New(store *graph.Store, bus *events.Bus, log *slog.Logger) *Ingest {
	return &Ingest{store: store, bus: bus, log: log.With("component", "emitters")}
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
		token := r.Header.Get(TokenHeader)
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
