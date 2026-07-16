package siterelay_test

import (
	"context"
	"io"
	"sync"

	"github.com/dstout-devops/stratt/core/internal/siterelay"
)

// In-memory Transport: proves the relay end-to-end WITHOUT NATS (the NATS-backed
// Transport is a thin adapter, slice 2). A memConn is one call's paired channels;
// the Dialer hands the server end to the Acceptor.

type memConn struct {
	c2s, s2c chan siterelay.Msg
	done     chan struct{}
	once     sync.Once
}

func (c *memConn) close() { c.once.Do(func() { close(c.done) }) }

type memEnd struct {
	conn *memConn
	send chan siterelay.Msg
	recv chan siterelay.Msg
}

func (e *memEnd) Send(m siterelay.Msg) error {
	select {
	case e.send <- m:
		return nil
	case <-e.conn.done:
		return io.ErrClosedPipe
	}
}

func (e *memEnd) Recv() (siterelay.Msg, error) {
	select {
	case m := <-e.recv:
		return m, nil
	case <-e.conn.done:
		return siterelay.Msg{}, io.EOF
	}
}

func (e *memEnd) Close() error { e.conn.close(); return nil }

type memDialer struct{ ch chan *memEnd }

func (d *memDialer) Open(ctx context.Context, _ string) (siterelay.CallStream, error) {
	conn := &memConn{c2s: make(chan siterelay.Msg, 16), s2c: make(chan siterelay.Msg, 16), done: make(chan struct{})}
	server := &memEnd{conn: conn, send: conn.s2c, recv: conn.c2s}
	select {
	case d.ch <- server:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &memEnd{conn: conn, send: conn.c2s, recv: conn.s2c}, nil // client end
}

type memAcceptor struct{ ch chan *memEnd }

func (a *memAcceptor) Accept(ctx context.Context) (siterelay.CallStream, error) {
	select {
	case s := <-a.ch:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func newMemTransport() (siterelay.Dialer, siterelay.Acceptor) {
	ch := make(chan *memEnd, 16)
	return &memDialer{ch}, &memAcceptor{ch}
}
