// Package sitegw is the hub↔Site NATS transport (charter §2.3, ADR-0032): the
// control plane's gateway for dispatching Run slices to remote execution loci
// and the primitives a Site's stratt-agent uses to receive them. It is the ONLY
// new NATS direction Sites add — task events already flow hub-ward over the NATS
// leaf into the existing run-events stream (events.Bus), unchanged.
//
// One library, both ends: the hub uses Dispatch/AwaitResult/Cancel/LiveSites;
// the agent uses ConsumeDispatch/PublishResult/SubscribeCancel/Heartbeat. Both
// share the stream/subject topology in siteproto so they cannot drift (§1.4).
//
// §2.5: a DispatchRequest carries credential POINTERS and a RemoteSafe JobSpec
// only — Dispatch re-checks RemoteSafe as defense in depth before publish.
package sitegw

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/dstout-devops/stratt/core/internal/dispatch"
	"github.com/dstout-devops/stratt/core/internal/siteproto"
)

// Gateway is a NATS connection to the dispatch/result plane. The hub holds one;
// each agent holds one against its local leaf.
type Gateway struct {
	nc  *nats.Conn
	js  jetstream.JetStream
	log *slog.Logger
}

// Connect dials NATS and returns a Gateway. name identifies the connection
// (e.g. "strattd" or "stratt-agent/<site>").
func Connect(url, name string, log *slog.Logger) (*Gateway, error) {
	nc, err := nats.Connect(url, nats.Name(name))
	if err != nil {
		return nil, fmt.Errorf("sitegw: connect nats: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("sitegw: jetstream: %w", err)
	}
	return &Gateway{nc: nc, js: js, log: log.With("component", "sitegw")}, nil
}

// Close drains the connection.
func (g *Gateway) Close() { g.nc.Close() }

// EnsureStreams creates the dispatch work-queue, the result stream, and the
// liveness KV (idempotent). The hub calls this at startup; a Site's leaf borrows
// the hub's JetStream, so the agent need not (and must not) re-create them.
func (g *Gateway) EnsureStreams(ctx context.Context) error {
	if _, err := g.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      siteproto.DispatchStream,
		Subjects:  []string{siteproto.DispatchStreamSubjects},
		Storage:   jetstream.FileStorage,
		Retention: jetstream.WorkQueuePolicy, // a dispatch is claimed once
		MaxAge:    24 * time.Hour,
	}); err != nil {
		return fmt.Errorf("sitegw: ensure dispatch stream: %w", err)
	}
	if _, err := g.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     siteproto.ResultStream,
		Subjects: []string{siteproto.ResultStreamSubjects},
		Storage:  jetstream.FileStorage,
		MaxAge:   24 * time.Hour,
	}); err != nil {
		return fmt.Errorf("sitegw: ensure result stream: %w", err)
	}
	if _, err := g.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: siteproto.LivenessBucket,
		TTL:    45 * time.Second, // a missed heartbeat expires the key
	}); err != nil {
		return fmt.Errorf("sitegw: ensure liveness kv: %w", err)
	}
	return nil
}

// ── Hub side ────────────────────────────────────────────────────────────────

// Dispatch publishes one work item to a Site (MsgID = runID/slice dedups a
// Temporal activity retry). RemoteSafe is re-checked here as a structural §2.5
// backstop — no plain Env material may ever be serialized to a Site.
func (g *Gateway) Dispatch(ctx context.Context, req siteproto.DispatchRequest) error {
	if err := req.Spec.RemoteSafe(); err != nil {
		return fmt.Errorf("sitegw: refusing to dispatch to site %q: %w", req.Site, err)
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("sitegw: marshal dispatch: %w", err)
	}
	if _, err := g.js.Publish(ctx, siteproto.DispatchSubject(req.Site), payload,
		jetstream.WithMsgID(fmt.Sprintf("%s/%d", req.RunID, req.Slice))); err != nil {
		return fmt.Errorf("sitegw: publish dispatch: %w", err)
	}
	return nil
}

// AwaitResult blocks until the Site publishes the terminal result for
// (runID, slice), heartbeating meanwhile so a long remote run keeps the hub
// activity alive. DeliverAll on the filtered subject replays a result that
// landed before this consumer started, so there is no publish/await race.
func (g *Gateway) AwaitResult(ctx context.Context, runID string, slice int, heartbeat func()) (siteproto.DispatchResult, error) {
	cons, err := g.js.OrderedConsumer(ctx, siteproto.ResultStream, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{siteproto.ResultSubject(runID, slice)},
		DeliverPolicy:  jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return siteproto.DispatchResult{}, fmt.Errorf("sitegw: result consumer: %w", err)
	}
	it, err := cons.Messages()
	if err != nil {
		return siteproto.DispatchResult{}, fmt.Errorf("sitegw: result messages: %w", err)
	}
	defer it.Stop()

	resCh := make(chan siteproto.DispatchResult, 1)
	errCh := make(chan error, 1)
	go func() {
		msg, err := it.Next()
		if err != nil {
			errCh <- err
			return
		}
		var dr siteproto.DispatchResult
		if err := json.Unmarshal(msg.Data(), &dr); err != nil {
			errCh <- fmt.Errorf("sitegw: decode result: %w", err)
			return
		}
		resCh <- dr
	}()

	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case dr := <-resCh:
			return dr, nil
		case err := <-errCh:
			if ctx.Err() != nil {
				return siteproto.DispatchResult{}, ctx.Err()
			}
			return siteproto.DispatchResult{}, err
		case <-ctx.Done():
			return siteproto.DispatchResult{}, ctx.Err()
		case <-t.C:
			if heartbeat != nil {
				heartbeat()
			}
		}
	}
}

// DispatchAndAwait dispatches one slice to a Site and returns its terminal
// dispatch.Result — the orchestrate.SiteGateway seam. A terminal Site-side error
// (e.g. a missing local Secret) surfaces as an error that fails the branch.
func (g *Gateway) DispatchAndAwait(ctx context.Context, req siteproto.DispatchRequest, heartbeat func()) (dispatch.Result, error) {
	if err := g.Dispatch(ctx, req); err != nil {
		return dispatch.Result{}, err
	}
	dr, err := g.AwaitResult(ctx, req.RunID, req.Slice, heartbeat)
	if err != nil {
		return dispatch.Result{}, err
	}
	if dr.Err != "" {
		return dispatch.Result{}, fmt.Errorf("site %s: %s", dr.Site, dr.Err)
	}
	return dr.Result, nil
}

// Cancel signals a Site to delete a Run's Jobs (ephemeral core-NATS publish;
// the agent's Job lease is the backstop if a partition drops it).
func (g *Gateway) Cancel(ctx context.Context, site, runID string) error {
	if err := g.nc.Publish(siteproto.CancelSubject(site), []byte(runID)); err != nil {
		return fmt.Errorf("sitegw: publish cancel: %w", err)
	}
	return g.nc.FlushWithContext(ctx)
}

// LiveSites returns the currently-heartbeating agents keyed by Site name (the
// KV TTL has already expired the dead ones).
func (g *Gateway) LiveSites(ctx context.Context) (map[string]siteproto.Liveness, error) {
	kv, err := g.js.KeyValue(ctx, siteproto.LivenessBucket)
	if err != nil {
		return nil, fmt.Errorf("sitegw: liveness kv: %w", err)
	}
	keys, err := kv.Keys(ctx)
	if err != nil {
		if err == jetstream.ErrNoKeysFound {
			return map[string]siteproto.Liveness{}, nil
		}
		return nil, fmt.Errorf("sitegw: liveness keys: %w", err)
	}
	out := make(map[string]siteproto.Liveness, len(keys))
	for _, k := range keys {
		entry, err := kv.Get(ctx, k)
		if err != nil {
			continue // raced with a TTL expiry
		}
		var l siteproto.Liveness
		if err := json.Unmarshal(entry.Value(), &l); err == nil {
			out[k] = l
		}
	}
	return out, nil
}

// ── Site (agent) side ─────────────────────────────────────────────────────────

// ConsumeDispatch delivers this Site's work items to fn through a durable
// pull-backed consumer (store-and-forward + redelivery on agent restart). fn
// returning nil ACKs; an error NAKs for redelivery. The caller ACKs only after
// publishing the result, so an agent crash mid-run redelivers the work.
func (g *Gateway) ConsumeDispatch(ctx context.Context, site string, fn func(context.Context, siteproto.DispatchRequest) error) error {
	cons, err := g.js.CreateOrUpdateConsumer(ctx, siteproto.DispatchStream, jetstream.ConsumerConfig{
		Durable:       "site-" + site,
		FilterSubject: siteproto.DispatchSubject(site),
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckWait:       30 * time.Minute, // longer than a realistic run
		MaxAckPending: 1,                // one run at a time per Site (v1)
	})
	if err != nil {
		return fmt.Errorf("sitegw: dispatch consumer: %w", err)
	}
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		var req siteproto.DispatchRequest
		if err := json.Unmarshal(msg.Data(), &req); err != nil {
			_ = msg.Term() // undecodable: never redeliver
			return
		}
		if err := fn(ctx, req); err != nil {
			g.log.Error("dispatch handler failed", "run", req.RunID, "slice", req.Slice, "err", err)
			_ = msg.Nak()
			return
		}
		_ = msg.Ack()
	})
	if err != nil {
		return fmt.Errorf("sitegw: consume dispatch: %w", err)
	}
	<-ctx.Done()
	cc.Stop()
	return ctx.Err()
}

// PublishResult reports a slice's terminal outcome to the hub (MsgID dedups a
// redelivered result).
func (g *Gateway) PublishResult(ctx context.Context, dr siteproto.DispatchResult) error {
	payload, err := json.Marshal(dr)
	if err != nil {
		return fmt.Errorf("sitegw: marshal result: %w", err)
	}
	if _, err := g.js.Publish(ctx, siteproto.ResultSubject(dr.RunID, dr.Slice), payload,
		jetstream.WithMsgID(fmt.Sprintf("%s/%d", dr.RunID, dr.Slice))); err != nil {
		return fmt.Errorf("sitegw: publish result: %w", err)
	}
	return nil
}

// SubscribeCancel invokes fn(runID) when the hub cancels a Run at this Site.
// Returns an unsubscribe func.
func (g *Gateway) SubscribeCancel(site string, fn func(runID string)) (func(), error) {
	sub, err := g.nc.Subscribe(siteproto.CancelSubject(site), func(m *nats.Msg) {
		fn(string(m.Data))
	})
	if err != nil {
		return nil, fmt.Errorf("sitegw: subscribe cancel: %w", err)
	}
	return func() { _ = sub.Unsubscribe() }, nil
}

// Heartbeat writes this agent's liveness into the TTL'd KV.
func (g *Gateway) Heartbeat(ctx context.Context, l siteproto.Liveness) error {
	kv, err := g.js.KeyValue(ctx, siteproto.LivenessBucket)
	if err != nil {
		return fmt.Errorf("sitegw: liveness kv: %w", err)
	}
	payload, err := json.Marshal(l)
	if err != nil {
		return fmt.Errorf("sitegw: marshal liveness: %w", err)
	}
	if _, err := kv.Put(ctx, l.Site, payload); err != nil {
		return fmt.Errorf("sitegw: put liveness: %w", err)
	}
	return nil
}
