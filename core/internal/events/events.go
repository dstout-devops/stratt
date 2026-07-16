// Package events is the Run event stream on NATS JetStream (charter §3).
// Every task event of every Run is published here and only here; Postgres
// keeps Run summaries, never events. The SSE tail (§3.1) and any Emitter
// machinery consume the same stream.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/dstout-devops/stratt/types"
)

const (
	// StreamName is the base Run-event stream name; a named Cell scopes it to
	// STRATT_RUN_EVENTS_<CELL> so peer Cells sharing a NATS cluster never
	// cross-wire each other's Run events (ADR-0044 slice 6).
	StreamName = "STRATT_RUN_EVENTS"
	// subjectPrefix + <runID> is the per-Run subject (base; Cell-scoped below).
	subjectPrefix = "stratt.run."
)

// Bus publishes and tails Run events over JetStream. Its stream/subject names
// are Cell-scoped at Connect: byte-identical for the built-in LocalCell (scope
// ""), Cell-prefixed for a named Cell (ADR-0044 slice 6).
type Bus struct {
	nc *nats.Conn
	js jetstream.JetStream
	// scope is the Cell scope token (types.CellScopeToken); "" ⇒ LocalCell.
	scope string
	// runStream and runSubj are the Cell-scoped Run-event names derived once.
	runStream string
	runSubj   string
	// emitter/notice stream + subject roots, Cell-scoped the same way.
	emitterStream string
	emitterSubj   string
	noticeStream  string
	noticeSubj    string
}

// Connect dials NATS and ensures the Run event stream exists. scope is the Cell
// scope token (types.CellScopeToken); "" for the single-Cell default keeps
// every stream/subject name byte-identical to the pre-Cells control plane.
func Connect(ctx context.Context, url, scope string) (*Bus, error) {
	nc, err := nats.Connect(url, nats.Name("strattd"))
	if err != nil {
		return nil, fmt.Errorf("events: connect nats: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("events: jetstream: %w", err)
	}
	b := &Bus{
		nc:            nc,
		js:            js,
		scope:         scope,
		runStream:     types.ScopedStream(StreamName, scope),
		runSubj:       types.ScopedSubjectRoot(subjectPrefix, scope),
		emitterStream: types.ScopedStream(EmitterStreamName, scope),
		emitterSubj:   types.ScopedSubjectRoot(emitterSubject, scope),
		noticeStream:  types.ScopedStream(NoticeStreamName, scope),
		noticeSubj:    types.ScopedSubjectRoot(noticeSubject, scope),
	}
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     b.runStream,
		Subjects: []string{b.runSubj + ">"},
		Storage:  jetstream.FileStorage,
		// Run events are replayable history for the descent ladder (§1.8);
		// retention limits become policy later, generous default now.
		MaxAge: 14 * 24 * time.Hour,
	})
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("events: ensure stream: %w", err)
	}
	return b, nil
}

// Close drains the connection.
func (b *Bus) Close() { b.nc.Close() }

// StreamName returns this Bus's Cell-scoped Run-event stream name (for logging).
func (b *Bus) StreamName() string { return b.runStream }

// Connected reports whether the NATS connection is currently up — the readiness
// signal for the event bus (ADR-0040).
func (b *Bus) Connected() bool { return b.nc.IsConnected() }

func (b *Bus) subject(runID string) string { return b.runSubj + runID }

// Publish appends one event to a Run's stream.
func (b *Bus) Publish(ctx context.Context, ev types.RunEvent) error {
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("events: marshal: %w", err)
	}
	// (RunID, Slice, Seq) is the event identity: Seq is the tool's
	// deterministic counter within one slice, so a re-publish (Temporal
	// activity retry re-following the same pod) dedups server-side inside
	// JetStream's dedup window, while parallel slices — whose tools all
	// count from 1 — never dedup each other away.
	if _, err := b.js.Publish(ctx, b.subject(ev.RunID), payload,
		jetstream.WithMsgID(fmt.Sprintf("%s/%d/%d", ev.RunID, ev.Slice, ev.Seq))); err != nil {
		return fmt.Errorf("events: publish: %w", err)
	}
	return nil
}

// Tail replays a Run's events from the beginning and follows until ctx ends.
// The full stream is always reachable — no event cap, no truncation (ADR-0003
// L2: the descent is never truncated).
func (b *Bus) Tail(ctx context.Context, runID string, fn func(types.RunEvent) error) error {
	cons, err := b.js.OrderedConsumer(ctx, b.runStream, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{b.subject(runID)},
		DeliverPolicy:  jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return fmt.Errorf("events: consumer: %w", err)
	}
	it, err := cons.Messages()
	if err != nil {
		return fmt.Errorf("events: messages: %w", err)
	}
	defer it.Stop()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
		}
		it.Stop()
	}()

	for {
		msg, err := it.Next()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("events: next: %w", err)
		}
		var ev types.RunEvent
		if err := json.Unmarshal(msg.Data(), &ev); err != nil {
			return fmt.Errorf("events: decode: %w", err)
		}
		if err := fn(ev); err != nil {
			return err
		}
	}
}
