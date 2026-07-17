package siterelay

import (
	"context"
	"io"
	"sync"
)

// InProcess returns a paired Dialer/Acceptor sharing an in-memory channel — a
// relay with no network. It is the transport for a co-located hub+agent (a
// single-binary dev deployment) and the substrate for tests: the same relay code
// path (Client ↔ Serve) runs without NATS, so the ADR-0049 keystone — host.go
// unchanged, agent governs nothing — is exercisable anywhere.
func InProcess() (Dialer, Acceptor) {
	ch := make(chan *memEnd, 16)
	return &memDialer{ch}, &memAcceptor{ch}
}

// memConn is one call's paired channels; the Dialer hands the server end to the
// Acceptor.
type memConn struct {
	c2s, s2c chan Msg
	done     chan struct{}
	once     sync.Once
}

func (c *memConn) close() { c.once.Do(func() { close(c.done) }) }

type memEnd struct {
	conn *memConn
	send chan Msg
	recv chan Msg
}

func (e *memEnd) Send(m Msg) error {
	select {
	case e.send <- m:
		return nil
	case <-e.conn.done:
		return io.ErrClosedPipe
	}
}

func (e *memEnd) Recv() (Msg, error) {
	// Drain buffered messages BEFORE honoring done — the peer's Close() closes the
	// shared `done` as soon as it finishes sending, and a naive select would race
	// the (still-buffered) terminal message against EOF (§1.8: never drop a result).
	select {
	case m := <-e.recv:
		return m, nil
	default:
	}
	select {
	case m := <-e.recv:
		return m, nil
	case <-e.conn.done:
		return Msg{}, io.EOF
	}
}

func (e *memEnd) Close() error { e.conn.close(); return nil }

type memDialer struct{ ch chan *memEnd }

func (d *memDialer) Open(ctx context.Context, _ string) (CallStream, error) {
	conn := &memConn{c2s: make(chan Msg, 16), s2c: make(chan Msg, 16), done: make(chan struct{})}
	server := &memEnd{conn: conn, send: conn.s2c, recv: conn.c2s}
	select {
	case d.ch <- server:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &memEnd{conn: conn, send: conn.c2s, recv: conn.s2c}, nil // client end
}

type memAcceptor struct{ ch chan *memEnd }

func (a *memAcceptor) Accept(ctx context.Context) (CallStream, error) {
	select {
	case s := <-a.ch:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
