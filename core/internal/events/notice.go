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
	// NoticeStreamName holds outbound Notices (ADR-0027) — a separate stream
	// from Run task events and Emitter events: different producers (the
	// orchestration activities), a different consumer (the notifier), a
	// different retention pressure.
	NoticeStreamName = "STRATT_NOTICES"
	noticeSubject    = "stratt.notice."
)

// EnsureNoticeStream creates the notice stream (idempotent).
func (b *Bus) EnsureNoticeStream(ctx context.Context) error {
	_, err := b.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     b.noticeStream,
		Subjects: []string{b.noticeSubj + ">"},
		Storage:  jetstream.FileStorage,
		MaxAge:   7 * 24 * time.Hour,
	})
	if err != nil {
		return fmt.Errorf("events: ensure notice stream: %w", err)
	}
	return nil
}

// NoticeHash is the content identity of one Notice — the dedup axis for the
// JetStream publish. It excludes At so a Temporal activity RETRY (which
// re-emits the same Notice with a fresh timestamp) dedups against the first
// publish inside JetStream's dedup window, exactly like EventHash for emitter
// events. Distinct occurrences (a later, genuinely-new Finding open) carry a
// different Subject and so publish independently.
func NoticeHash(n types.Notice) string {
	doc, _ := json.Marshal(struct {
		Kind    string         `json:"kind"`
		Subject string         `json:"subject"`
		Payload map[string]any `json:"payload"`
	}{n.Kind, n.Subject, n.Payload})
	sum := sha256.Sum256(doc)
	return hex.EncodeToString(sum[:])
}

// PublishNotice appends one Notice to the notice stream.
func (b *Bus) PublishNotice(ctx context.Context, n types.Notice) error {
	if n.At.IsZero() {
		n.At = time.Now().UTC()
	}
	payload, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("events: marshal notice: %w", err)
	}
	if _, err := b.js.Publish(ctx, b.noticeSubj+n.Kind, payload,
		jetstream.WithMsgID(NoticeHash(n))); err != nil {
		return fmt.Errorf("events: publish notice: %w", err)
	}
	return nil
}

// ConsumeNotices delivers Notices to fn through a durable consumer
// (at-least-once; the notifier's deliveries tolerate rare duplicates —
// ADR-0027). fn returning nil acks; an error naks for redelivery.
func (b *Bus) ConsumeNotices(ctx context.Context, durable string, fn func(types.Notice) error) error {
	cons, err := b.js.CreateOrUpdateConsumer(ctx, b.noticeStream, jetstream.ConsumerConfig{
		Durable:       durable,
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverNewPolicy,
	})
	if err != nil {
		return fmt.Errorf("events: notice consumer: %w", err)
	}
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		var n types.Notice
		if err := json.Unmarshal(msg.Data(), &n); err != nil {
			_ = msg.Term() // undecodable: never redeliver
			return
		}
		if err := fn(n); err != nil {
			_ = msg.Nak()
			return
		}
		_ = msg.Ack()
	})
	if err != nil {
		return fmt.Errorf("events: consume notices: %w", err)
	}
	<-ctx.Done()
	cc.Stop()
	return ctx.Err()
}
