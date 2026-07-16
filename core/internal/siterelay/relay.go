// Package siterelay tunnels the sovereign plugin port to a plugin running at a
// remote Site (ADR-0049). It is an AUTHENTICATED TRANSPORT RELAY, never a governor:
// the hub's pluginhost.Host is unchanged — it drives a relay PluginServiceClient
// whose calls are forwarded, verb-by-verb, over a message Transport to the Site
// agent, which proxies them to the Site-local plugin's real client. Every
// governance step (grant-match, identity/facet/label gating, the confused-deputy
// target gate, the Succeeded fold, provenance stamping, plan hashing) runs HUB-side
// over the plugin's raw, provenance-free wire shapes. The relay marshals only
// opaque proto bytes; it interprets nothing and stamps nothing (ADR-0049 V1).
//
// The Transport seam is deliberately message-level (not a raw byte tunnel): it maps
// cleanly onto the existing JetStream/NATS leaf (per-call subjects) and is
// exercisable in-memory without NATS, so the keystone claim — host.go unchanged,
// agent governs nothing — is unit-testable. The NATS-backed Transport is a thin
// adapter (slice 2).
package siterelay

import (
	"context"
	"errors"
	"io"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// Msg is one relay envelope. Payload is OPAQUE marshaled proto (a request or one
// streamed response) — the relay never inspects it for governance (ADR-0049 V1).
type Msg struct {
	Method   string // set on the opening request; names the port verb
	Payload  []byte // marshaled proto request/response
	Terminal bool   // last message of the call (the gRPC EOF boundary)
	Err      string // terminal error text; "" on a clean end
	Cancel   bool   // client→agent: cancel this in-flight call
}

// CallStream is one relay call's bidirectional message channel — the client view
// sends the request (then optional cancel) and receives the reply/stream; the
// server (agent) view is the mirror. Per-call (never multiplexed) so each verb is
// independent and maps to a NATS reply subject.
type CallStream interface {
	Send(Msg) error
	Recv() (Msg, error) // returns io.EOF-style completion via Msg.Terminal
	Close() error
}

// Dialer opens a call stream to a Site (hub side).
type Dialer interface {
	Open(ctx context.Context, method string) (CallStream, error)
}

// Acceptor yields incoming call streams at the Site (agent side).
type Acceptor interface {
	Accept(ctx context.Context) (CallStream, error)
}

// Verb method names on the wire (stable relay routing keys; NOT governance).
const (
	mGetManifest = "GetManifest"
	mHealth      = "Health"
	mPlan        = "Plan"
	mObserve     = "Observe"
	mApply       = "Apply"
	mDestroy     = "Destroy"
	mInvoke      = "Invoke"
	mSubscribe   = "Subscribe"
)

// Serve runs the Site-agent side: it accepts relayed calls and proxies each to the
// Site-local plugin's real client, streaming responses back. It GOVERNS NOTHING —
// it forwards opaque proto bytes and never constructs a grant, gates an emission,
// or stamps provenance (ADR-0049 V1). Blocks until ctx is done or Accept fails.
func Serve(ctx context.Context, acc Acceptor, plugin pluginv1.PluginServiceClient) error {
	for {
		cs, err := acc.Accept(ctx)
		if err != nil {
			return err
		}
		go serveCall(ctx, cs, plugin)
	}
}

func serveCall(ctx context.Context, cs CallStream, plugin pluginv1.PluginServiceClient) {
	defer cs.Close()
	req, err := cs.Recv()
	if err != nil {
		return
	}
	// A cancel arriving on the same stream aborts the proxied call.
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		for {
			m, err := cs.Recv()
			if err != nil {
				return
			}
			if m.Cancel {
				cancel()
				return
			}
		}
	}()

	switch req.Method {
	case mGetManifest:
		unaryProxy[pluginv1.GetManifestRequest, pluginv1.GetManifestResponse](cctx, cs, req, plugin.GetManifest)
	case mHealth:
		unaryProxy[pluginv1.HealthRequest, pluginv1.HealthResponse](cctx, cs, req, plugin.Health)
	case mPlan:
		unaryProxy[pluginv1.PlanRequest, pluginv1.PlanResponse](cctx, cs, req, plugin.Plan)
	case mObserve:
		streamProxy[pluginv1.ObserveRequest, pluginv1.ObserveResponse](cctx, cs, req, plugin.Observe)
	case mApply:
		streamProxy[pluginv1.ApplyRequest, pluginv1.ApplyResponse](cctx, cs, req, plugin.Apply)
	case mDestroy:
		streamProxy[pluginv1.DestroyRequest, pluginv1.DestroyResponse](cctx, cs, req, plugin.Destroy)
	case mInvoke:
		streamProxy[pluginv1.InvokeRequest, pluginv1.InvokeResponse](cctx, cs, req, plugin.Invoke)
	case mSubscribe:
		streamProxy[pluginv1.SubscribeRequest, pluginv1.SubscribeResponse](cctx, cs, req, plugin.Subscribe)
	default:
		_ = cs.Send(Msg{Terminal: true, Err: "siterelay: unknown method " + req.Method})
	}
}

// unaryProxy forwards a unary verb: unmarshal the opaque request, call the local
// plugin, marshal the opaque response back as the terminal message.
func unaryProxy[Req any, Resp any, PReq protoPtr[Req], PResp protoPtr[Resp]](
	ctx context.Context, cs CallStream, req Msg,
	call func(context.Context, PReq, ...grpc.CallOption) (PResp, error),
) {
	in := PReq(new(Req))
	if err := proto.Unmarshal(req.Payload, in); err != nil {
		_ = cs.Send(Msg{Terminal: true, Err: "siterelay: bad request: " + err.Error()})
		return
	}
	out, err := call(ctx, in)
	if err != nil {
		_ = cs.Send(Msg{Terminal: true, Err: err.Error()})
		return
	}
	b, err := proto.Marshal(out)
	if err != nil {
		_ = cs.Send(Msg{Terminal: true, Err: "siterelay: bad response: " + err.Error()})
		return
	}
	_ = cs.Send(Msg{Payload: b, Terminal: true})
}

// streamProxy forwards a server-streaming verb: each plugin response becomes one
// relay message; the plugin's io.EOF becomes a clean terminal, an error a terminal
// with Err.
func streamProxy[Req any, Resp any, PReq protoPtr[Req], PResp protoPtr[Resp]](
	ctx context.Context, cs CallStream, req Msg,
	call func(context.Context, PReq, ...grpc.CallOption) (grpc.ServerStreamingClient[Resp], error),
) {
	in := PReq(new(Req))
	if err := proto.Unmarshal(req.Payload, in); err != nil {
		_ = cs.Send(Msg{Terminal: true, Err: "siterelay: bad request: " + err.Error()})
		return
	}
	stream, err := call(ctx, in)
	if err != nil {
		_ = cs.Send(Msg{Terminal: true, Err: err.Error()})
		return
	}
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			_ = cs.Send(Msg{Terminal: true})
			return
		}
		if err != nil {
			_ = cs.Send(Msg{Terminal: true, Err: err.Error()})
			return
		}
		b, merr := proto.Marshal(PResp(resp)) // *Resp → the proto.Message-satisfying pointer
		if merr != nil {
			_ = cs.Send(Msg{Terminal: true, Err: "siterelay: bad response: " + merr.Error()})
			return
		}
		if serr := cs.Send(Msg{Payload: b}); serr != nil {
			return // hub went away / ctx cancelled
		}
	}
}

// protoPtr constrains a pointer-to-T that is a proto.Message (the standard Go
// generics proto idiom).
type protoPtr[T any] interface {
	*T
	proto.Message
}

// statusErr renders a terminal relay Err as a gRPC-style error the pluginhost sees
// exactly as a direct-dial failure.
func statusErr(msg string) error { return status.Error(codes.Unavailable, msg) }
