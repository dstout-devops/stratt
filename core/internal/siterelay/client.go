package siterelay

import (
	"context"
	"io"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// Client is the hub-side relay: a pluginv1.PluginServiceClient whose verbs are
// forwarded over a Dialer to a Site agent. It is a drop-in for a direct gRPC
// client, so pluginhost.Host drives a Site plugin with ZERO changes (ADR-0049).
type Client struct {
	dialer Dialer
}

// NewClient wraps a Dialer as a PluginServiceClient. The Dialer is the transport
// (NATS in production, in-memory in tests).
func NewClient(d Dialer) pluginv1.PluginServiceClient { return &Client{dialer: d} }

func (c *Client) unary(ctx context.Context, method string, in, out proto.Message) error {
	cs, err := c.dialer.Open(ctx, method)
	if err != nil {
		return statusErr(err.Error())
	}
	defer cs.Close()
	b, err := proto.Marshal(in)
	if err != nil {
		return err
	}
	if err := cs.Send(Msg{Method: method, Payload: b}); err != nil {
		return statusErr(err.Error())
	}
	m, err := cs.Recv()
	if err != nil {
		return statusErr(err.Error())
	}
	if m.Err != "" {
		return statusErr(m.Err)
	}
	return proto.Unmarshal(m.Payload, out)
}

func (c *Client) GetManifest(ctx context.Context, in *pluginv1.GetManifestRequest, _ ...grpc.CallOption) (*pluginv1.GetManifestResponse, error) {
	out := &pluginv1.GetManifestResponse{}
	return out, c.unary(ctx, mGetManifest, in, out)
}

func (c *Client) Health(ctx context.Context, in *pluginv1.HealthRequest, _ ...grpc.CallOption) (*pluginv1.HealthResponse, error) {
	out := &pluginv1.HealthResponse{}
	return out, c.unary(ctx, mHealth, in, out)
}

func (c *Client) Plan(ctx context.Context, in *pluginv1.PlanRequest, _ ...grpc.CallOption) (*pluginv1.PlanResponse, error) {
	out := &pluginv1.PlanResponse{}
	return out, c.unary(ctx, mPlan, in, out)
}

func (c *Client) Observe(ctx context.Context, in *pluginv1.ObserveRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[pluginv1.ObserveResponse], error) {
	return openStream[pluginv1.ObserveResponse, *pluginv1.ObserveResponse](ctx, c.dialer, mObserve, in)
}

func (c *Client) Apply(ctx context.Context, in *pluginv1.ApplyRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[pluginv1.ApplyResponse], error) {
	return openStream[pluginv1.ApplyResponse, *pluginv1.ApplyResponse](ctx, c.dialer, mApply, in)
}

func (c *Client) Destroy(ctx context.Context, in *pluginv1.DestroyRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[pluginv1.DestroyResponse], error) {
	return openStream[pluginv1.DestroyResponse, *pluginv1.DestroyResponse](ctx, c.dialer, mDestroy, in)
}

func (c *Client) Invoke(ctx context.Context, in *pluginv1.InvokeRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[pluginv1.InvokeResponse], error) {
	return openStream[pluginv1.InvokeResponse, *pluginv1.InvokeResponse](ctx, c.dialer, mInvoke, in)
}

func (c *Client) Subscribe(ctx context.Context, in *pluginv1.SubscribeRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[pluginv1.SubscribeResponse], error) {
	return openStream[pluginv1.SubscribeResponse, *pluginv1.SubscribeResponse](ctx, c.dialer, mSubscribe, in)
}

// openStream opens a call, sends the request, and returns a streaming client whose
// Recv() unmarshals relayed messages until the terminal boundary.
func openStream[Resp any, PResp protoPtr[Resp]](ctx context.Context, d Dialer, method string, in proto.Message) (grpc.ServerStreamingClient[Resp], error) {
	cs, err := d.Open(ctx, method)
	if err != nil {
		return nil, statusErr(err.Error())
	}
	b, err := proto.Marshal(in)
	if err != nil {
		_ = cs.Close()
		return nil, err
	}
	if err := cs.Send(Msg{Method: method, Payload: b}); err != nil {
		_ = cs.Close()
		return nil, statusErr(err.Error())
	}
	return &relayStream[Resp, PResp]{cs: cs, ctx: ctx}, nil
}

// relayStream implements grpc.ServerStreamingClient[Resp] over a CallStream.
type relayStream[Resp any, PResp protoPtr[Resp]] struct {
	grpc.ClientStream // unused surface (Header/Trailer/CloseSend/SendMsg/RecvMsg) — pluginhost calls only Recv/Context
	cs                CallStream
	ctx               context.Context
}

func (s *relayStream[Resp, PResp]) Recv() (*Resp, error) {
	m, err := s.cs.Recv()
	if err != nil {
		_ = s.cs.Close()
		return nil, statusErr(err.Error())
	}
	if m.Terminal {
		_ = s.cs.Close()
		if m.Err != "" {
			return nil, statusErr(m.Err)
		}
		return nil, io.EOF
	}
	out := PResp(new(Resp))
	if err := proto.Unmarshal(m.Payload, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *relayStream[Resp, PResp]) Context() context.Context { return s.ctx }
