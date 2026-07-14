package salt

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dstout-devops/stratt/core/internal/connectors/salt/saltsim"
	"github.com/dstout-devops/stratt/core/internal/events"
	"github.com/dstout-devops/stratt/types"
)

// capturePub is a fake eventPublisher that channels published events, so the
// Emitter is testable with no NATS.
type capturePub struct{ ch chan types.EmitterEvent }

func (c *capturePub) PublishEmitterEvent(_ context.Context, ev types.EmitterEvent) error {
	c.ch <- ev
	return nil
}

func recv(t *testing.T, ch chan types.EmitterEvent) types.EmitterEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a published event")
		return types.EmitterEvent{}
	}
}

// TestSaltEmitterStreamsAndTranslates proves the stream-subscriber Emitter:
// consume salt-api SSE, translate to EmitterEvents on the shared stream (via a
// capturing publisher), apply the tag filter, expose CEL-visible payload keys,
// and keep genuinely-distinct events dedup-safe.
func TestSaltEmitterStreamsAndTranslates(t *testing.T) {
	sim := saltsim.New()
	srv := httptest.NewServer(sim.Handler())
	defer srv.Close()

	pub := &capturePub{ch: make(chan types.EmitterEvent, 16)}
	cfg := Config{APIURL: srv.URL, Username: "u", Password: "p", EmitterName: "salt", EventTags: []string{"salt/minion/"}}
	em := NewEmitter(cfg, pub, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = em.Run(ctx) }()

	// A matching event, a filtered-out event, then a second matching event.
	sim.EmitEvent("salt/minion/web-01/start", map[string]any{"_stamp": "2026-07-14T00:00:00.1", "id": "web-01"})
	sim.EmitEvent("salt/job/20260714/ret", map[string]any{"_stamp": "2026-07-14T00:00:00.2", "fun": "test.ping"})
	sim.EmitEvent("salt/minion/db-01/start", map[string]any{"_stamp": "2026-07-14T00:00:00.3", "id": "db-01"})

	first := recv(t, pub.ch)
	second := recv(t, pub.ch)

	// The filtered salt/job event must never arrive: both received are minions.
	for _, ev := range []types.EmitterEvent{first, second} {
		tag, _ := ev.Payload["tag"].(string)
		if ev.Emitter != "salt" {
			t.Fatalf("wrong emitter name: %q", ev.Emitter)
		}
		if len(tag) < len("salt/minion/") || tag[:len("salt/minion/")] != "salt/minion/" {
			t.Fatalf("filtered tag leaked through: %q", tag)
		}
		if _, ok := ev.Payload["data"].(map[string]any); !ok {
			t.Fatalf("payload.data must be a map for CEL event.data.*: %v", ev.Payload)
		}
		if ev.Payload["stamp"] == "" {
			t.Fatalf("payload.stamp missing (needed for dedup + CEL): %v", ev.Payload)
		}
	}

	// No third event within a short window (the job event was dropped).
	select {
	case ev := <-pub.ch:
		t.Fatalf("unexpected extra event (filter leaked?): %v", ev.Payload["tag"])
	case <-time.After(300 * time.Millisecond):
	}

	// Distinct stamps → distinct EventHash (dedup-safe on the JetStream stream).
	if events.EventHash(first) == events.EventHash(second) {
		t.Fatal("distinct events must hash distinctly or JetStream dedup drops the second")
	}
}
