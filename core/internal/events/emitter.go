package events

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/dstout-devops/stratt/types"
)

const (
	// EmitterStreamName holds ingested Emitter events (ADR-0018) — a
	// separate stream from Run task events: different producers, different
	// consumers, different retention pressure.
	EmitterStreamName = "STRATT_EMITTER_EVENTS"
	emitterSubject    = "stratt.emitter."
)

// EnsureEmitterStream creates the emitter event stream (idempotent).
func (b *Bus) EnsureEmitterStream(ctx context.Context) error {
	_, err := b.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     EmitterStreamName,
		Subjects: []string{emitterSubject + ">"},
		Storage:  jetstream.FileStorage,
		MaxAge:   7 * 24 * time.Hour,
	})
	if err != nil {
		return fmt.Errorf("events: ensure emitter stream: %w", err)
	}
	return nil
}

// EventHash is the content identity of one emitter event — the dedup axis
// for JetStream publishes and derived Temporal workflow ids (ADR-0018).
// Deliberately excludes ReceivedAt: callers like Alertmanager RETRY posts,
// and a retry must dedup. A genuinely new occurrence of an identical
// payload still fires later — JetStream's dedup window is short and
// Temporal only rejects the id while the prior launch is running.
func EventHash(ev types.EmitterEvent) string {
	doc, _ := json.Marshal(ev.Payload)
	sum := sha256.Sum256(append([]byte(ev.Emitter+"|"), doc...))
	return hex.EncodeToString(sum[:])
}

// PublishEmitterEvent appends one ingested event.
func (b *Bus) PublishEmitterEvent(ctx context.Context, ev types.EmitterEvent) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("events: marshal emitter event: %w", err)
	}
	if _, err := b.js.Publish(ctx, emitterSubject+ev.Emitter, payload,
		jetstream.WithMsgID(EventHash(ev))); err != nil {
		return fmt.Errorf("events: publish emitter event: %w", err)
	}
	return nil
}

// ConsumeEmitterEvents delivers events to fn through a durable consumer
// (at-least-once; fn's launches must be idempotent — ADR-0018 derives
// Temporal workflow ids from the event hash so redelivery cannot
// double-launch). fn returning nil acks; an error naks for redelivery.
func (b *Bus) ConsumeEmitterEvents(ctx context.Context, durable string, fn func(types.EmitterEvent) error) error {
	cons, err := b.js.CreateOrUpdateConsumer(ctx, EmitterStreamName, jetstream.ConsumerConfig{
		Durable:       durable,
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverNewPolicy,
	})
	if err != nil {
		return fmt.Errorf("events: emitter consumer: %w", err)
	}
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		var ev types.EmitterEvent
		if err := json.Unmarshal(msg.Data(), &ev); err != nil {
			_ = msg.Term() // undecodable: never redeliver
			return
		}
		if err := fn(ev); err != nil {
			_ = msg.Nak()
			return
		}
		_ = msg.Ack()
	})
	if err != nil {
		return fmt.Errorf("events: consume emitter events: %w", err)
	}
	<-ctx.Done()
	cc.Stop()
	return ctx.Err()
}
