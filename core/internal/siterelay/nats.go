package siterelay

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/nats-io/nats.go"
)

// The NATS-backed Transport (ADR-0049 slice 2). It carries the relay's per-call
// CallStreams over the SAME outbound NATS leaf the Site agent already holds — the
// hub INITIATES each call (NATS decouples connection-initiation from
// message-direction, so no inbound to the Site). Core NATS + per-call inbox
// subjects + a per-response sequence: a dropped message is DETECTED (seq gap) and
// fails the call VISIBLY (§1.8), never silently corrupts; the reconcile loop is the
// recovery mechanism (§1.6). JetStream durability for the opening dispatch is a
// documented hardening option, unneeded for these synchronous, retryable calls.
//
// Subjects: the opening request → `STRATT_SITERELAY.call.<site>.<plugin>` with
// Reply set to a fresh inbox; the agent streams responses to that inbox; the hub
// sends follow-ups (cancel) to `<inbox>.c2s`.

const callSubjectRoot = "STRATT_SITERELAY.call"

// CallSubject is the opening-request subject for one plugin at one Site — a Site
// may run several plugins (opentofu, ansible…), so calls route per-(site, plugin)
// and each plugin's agent-side Serve subscribes to its own subject.
func CallSubject(site, plugin string) string { return callSubjectRoot + "." + site + "." + plugin }

// wireFrame is the on-wire envelope: Seq orders/deduplicates the response stream (0
// on the opening request; 1..N on responses). Msg.Payload stays OPAQUE proto bytes
// — the transport marshals framing, never governance (ADR-0049 V1).
type wireFrame struct {
	Seq uint64 `json:"seq"`
	Msg Msg    `json:"msg"`
}

func encodeFrame(seq uint64, m Msg) ([]byte, error) { return json.Marshal(wireFrame{Seq: seq, Msg: m}) }

func decodeFrame(b []byte) (uint64, Msg, error) {
	var f wireFrame
	if err := json.Unmarshal(b, &f); err != nil {
		return 0, Msg{}, err
	}
	return f.Seq, f.Msg, nil
}

// ── hub side ────────────────────────────────────────────────────────────────

// NATSDialer opens relay calls to one Site over NATS (hub side).
type NATSDialer struct {
	nc     *nats.Conn
	site   string
	plugin string
}

// NewNATSDialer targets one plugin at one Site (plugin = the grant's plugin id).
func NewNATSDialer(nc *nats.Conn, site, plugin string) *NATSDialer {
	return &NATSDialer{nc: nc, site: site, plugin: plugin}
}

func (d *NATSDialer) Open(ctx context.Context, _ string) (CallStream, error) {
	inbox := nats.NewInbox()
	sub, err := d.nc.SubscribeSync(inbox)
	if err != nil {
		return nil, err
	}
	return &natsClientStream{nc: d.nc, site: d.site, plugin: d.plugin, inbox: inbox, sub: sub, ctx: ctx}, nil
}

type natsClientStream struct {
	nc     *nats.Conn
	site   string
	plugin string
	inbox  string
	sub    *nats.Subscription
	ctx    context.Context
	opened bool
	expect uint64 // last accepted response seq
}

func (s *natsClientStream) Send(m Msg) error {
	frame, err := encodeFrame(0, m)
	if err != nil {
		return err
	}
	if !s.opened {
		s.opened = true
		// Opening request → the Site's call subject, Reply = this call's inbox.
		return s.nc.PublishMsg(&nats.Msg{Subject: CallSubject(s.site, s.plugin), Reply: s.inbox, Data: frame})
	}
	// Follow-up (cancel) → the client→server subject.
	return s.nc.Publish(s.inbox+".c2s", frame)
}

func (s *natsClientStream) Recv() (Msg, error) {
	msg, err := s.sub.NextMsgWithContext(s.ctx)
	if err != nil {
		return Msg{}, err
	}
	seq, m, err := decodeFrame(msg.Data)
	if err != nil {
		return Msg{}, err
	}
	// Drop detection (§1.8): responses must arrive strictly 1,2,3… A gap means a
	// message was dropped — fail the call visibly rather than silently miss a
	// write-back / ItemResult.
	s.expect++
	if seq != s.expect {
		return Msg{}, fmt.Errorf("siterelay: response seq gap: got %d want %d (message dropped in transit)", seq, s.expect)
	}
	return m, nil
}

func (s *natsClientStream) Close() error {
	// Best-effort cancel so a Site-local call stops when the hub abandons it
	// (the per-call ctx is the backstop). Harmless after a normal terminal.
	if frame, err := encodeFrame(0, Msg{Cancel: true}); err == nil {
		_ = s.nc.Publish(s.inbox+".c2s", frame)
	}
	return s.sub.Unsubscribe()
}

// ── agent side ──────────────────────────────────────────────────────────────

// NATSAcceptor yields incoming relay calls for one plugin at this Site (agent side).
type NATSAcceptor struct {
	nc     *nats.Conn
	site   string
	plugin string
	once   sync.Once
	sub    *nats.Subscription
	err    error
}

// NewNATSAcceptor accepts calls for one plugin at this Site (plugin = its grant id).
func NewNATSAcceptor(nc *nats.Conn, site, plugin string) *NATSAcceptor {
	return &NATSAcceptor{nc: nc, site: site, plugin: plugin}
}

func (a *NATSAcceptor) Accept(ctx context.Context) (CallStream, error) {
	a.once.Do(func() { a.sub, a.err = a.nc.SubscribeSync(CallSubject(a.site, a.plugin)) })
	if a.err != nil {
		return nil, a.err
	}
	msg, err := a.sub.NextMsgWithContext(ctx)
	if err != nil {
		return nil, err
	}
	_, req, err := decodeFrame(msg.Data)
	if err != nil {
		return nil, err
	}
	c2s, err := a.nc.SubscribeSync(msg.Reply + ".c2s")
	if err != nil {
		return nil, err
	}
	return &natsServerStream{nc: a.nc, inbox: msg.Reply, request: req, c2s: c2s, ctx: ctx}, nil
}

type natsServerStream struct {
	nc      *nats.Conn
	inbox   string
	request Msg
	got     bool
	c2s     *nats.Subscription
	ctx     context.Context
	seq     uint64
}

func (s *natsServerStream) Recv() (Msg, error) {
	if !s.got {
		s.got = true // serveCall's first Recv returns the opening request
		return s.request, nil
	}
	msg, err := s.c2s.NextMsgWithContext(s.ctx)
	if err != nil {
		return Msg{}, err
	}
	_, m, err := decodeFrame(msg.Data)
	return m, err
}

func (s *natsServerStream) Send(m Msg) error {
	s.seq++
	frame, err := encodeFrame(s.seq, m)
	if err != nil {
		return err
	}
	return s.nc.Publish(s.inbox, frame)
}

func (s *natsServerStream) Close() error {
	if s.c2s != nil {
		return s.c2s.Unsubscribe()
	}
	return nil
}
