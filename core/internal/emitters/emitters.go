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
	"io"
	"log/slog"
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
// alertmanager = one event per alerts[] entry (payload = alert + shared
// group fields) so CEL rules match per alert.
func explode(em types.Emitter, body []byte) ([]types.EmitterEvent, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	switch em.Kind {
	case types.EmitterAlertmanager:
		var am struct {
			Receiver          string           `json:"receiver"`
			Status            string           `json:"status"`
			GroupLabels       map[string]any   `json:"groupLabels"`
			CommonLabels      map[string]any   `json:"commonLabels"`
			CommonAnnotations map[string]any   `json:"commonAnnotations"`
			Alerts            []map[string]any `json:"alerts"`
		}
		if err := json.Unmarshal(body, &am); err != nil {
			return nil, err
		}
		out := make([]types.EmitterEvent, 0, len(am.Alerts))
		for _, alert := range am.Alerts {
			payload := map[string]any{}
			for k, v := range alert {
				payload[k] = v
			}
			payload["receiver"] = am.Receiver
			payload["groupLabels"] = am.GroupLabels
			out = append(out, types.EmitterEvent{Emitter: em.Name, ReceivedAt: now, Payload: payload})
		}
		return out, nil
	default: // webhook
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
		return []types.EmitterEvent{{Emitter: em.Name, ReceivedAt: now, Payload: payload}}, nil
	}
}

// Explode is exported for tests.
func Explode(ctx context.Context, em types.Emitter, body []byte) ([]types.EmitterEvent, error) {
	_ = ctx
	return explode(em, body)
}
