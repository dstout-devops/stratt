package salt

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/dstout-devops/stratt/types"
)

// eventPublisher is the seam onto the emitter-event stream. *events.Bus
// satisfies it; tests pass a capturing fake (no NATS in unit tests).
type eventPublisher interface {
	PublishEmitterEvent(ctx context.Context, ev types.EmitterEvent) error
}

// Emitter is the Connector's event-producer capability (§2.2) — the first
// STREAM-SUBSCRIBER Emitter: it outbound-connects to salt-api GET /events (SSE),
// translates each Salt event, and publishes it onto the SAME emitter-event
// stream the inbound webhook Emitters use, so the existing Trigger engine
// consumes it unchanged (CEL match -> launch). One event model, one trigger
// spine (§1.6). No trigger/emitter-registry change is needed — Triggers match an
// emitter by name string.
type Emitter struct {
	cfg    Config
	pub    eventPublisher
	log    *slog.Logger
	client *saltClient
}

// NewEmitter prepares the Salt event-bus Emitter.
func NewEmitter(cfg Config, pub eventPublisher, log *slog.Logger) *Emitter {
	return &Emitter{cfg: cfg, pub: pub, log: log.With("connector", "salt", "emitter", cfg.emitterName())}
}

func (c Config) emitterName() string {
	switch {
	case c.EmitterName != "":
		return c.EmitterName
	case c.SourceName != "":
		return c.SourceName
	default:
		return "salt"
	}
}

// Run streams the Salt event bus until ctx ends, reconnecting with backoff on
// any stream error (master restart, token expiry) — never silently stops.
func (e *Emitter) Run(ctx context.Context) error {
	e.client = newSaltClient(e.cfg)
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := e.stream(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			e.log.Warn("event stream ended; reconnecting", "error", err, "backoff", backoff.String())
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// stream opens one /events SSE connection and processes frames until it ends.
func (e *Emitter) stream(ctx context.Context) error {
	token, err := e.client.authToken(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.cfg.APIURL+"/events", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-Auth-Token", token)

	res, err := e.client.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close() //nolint:errcheck
	if res.StatusCode == http.StatusUnauthorized {
		e.client.mu.Lock()
		e.client.token = "" // force re-login next reconnect
		e.client.mu.Unlock()
		return errUnauthorized
	}
	if res.StatusCode != http.StatusOK {
		return &statusError{res.Status}
	}

	// SSE frames: "tag: <tag>\ndata: <json>\n\n". Accumulate until a blank line.
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // events can be large
	var tag, data string
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Text()
		switch {
		case line == "":
			if tag != "" {
				e.handle(ctx, tag, data)
			}
			tag, data = "", ""
		case strings.HasPrefix(line, "tag:"):
			tag = strings.TrimSpace(strings.TrimPrefix(line, "tag:"))
		case strings.HasPrefix(line, "data:"):
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	return scanner.Err()
}

// handle translates one Salt event and publishes it, if its tag passes the
// filter. The Payload keys are top-level so CEL `when` expressions on Triggers
// see event.tag / event.stamp / event.data.* (the rules binding).
func (e *Emitter) handle(ctx context.Context, tag, dataJSON string) {
	if !e.tagMatches(tag) {
		return
	}
	data := map[string]any{}
	if dataJSON != "" {
		if err := json.Unmarshal([]byte(dataJSON), &data); err != nil {
			e.log.Warn("skipping event; bad data json", "tag", tag, "error", err)
			return
		}
	}
	// _stamp makes genuinely-distinct events hash distinctly (EventHash excludes
	// ReceivedAt), so the JetStream dedup window won't drop them.
	ev := types.EmitterEvent{
		Emitter:    e.cfg.emitterName(),
		ReceivedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Payload: map[string]any{
			"tag":   tag,
			"stamp": str(data, "_stamp"),
			"data":  data,
		},
	}
	if err := e.pub.PublishEmitterEvent(ctx, ev); err != nil {
		e.log.Error("publish salt event failed", "tag", tag, "error", err)
	}
}

// tagMatches applies the configured tag-prefix filter (empty = forward all).
func (e *Emitter) tagMatches(tag string) bool {
	if len(e.cfg.EventTags) == 0 {
		return true
	}
	for _, p := range e.cfg.EventTags {
		if strings.HasPrefix(tag, p) {
			return true
		}
	}
	return false
}

type statusError struct{ status string }

func (s *statusError) Error() string { return "salt: /events: " + s.status }

var errUnauthorized = &statusError{"401 Unauthorized (token cleared for re-login)"}
