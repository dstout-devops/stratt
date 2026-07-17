package siterelay_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/dstout-devops/stratt/core/internal/pluginhost"
	"github.com/dstout-devops/stratt/core/internal/siterelay"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
	"github.com/dstout-devops/stratt/types"
)

// sitePlugin is a canned Actuator "running at the Site": on Apply it streams a
// write-back entity carrying a granted (vcenter.uuid) and an ungranted-for-
// community (dns.fqdn) identity scheme, then a terminal CHANGED result.
type sitePlugin struct {
	pluginv1.UnimplementedPluginServiceServer
}

func (sitePlugin) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId: "vcenter-dev", ProtocolVersion: "v1", Class: pluginv1.PluginClass_PLUGIN_CLASS_ACTUATOR,
	}}, nil
}

func (sitePlugin) Apply(_ *pluginv1.ApplyRequest, stream grpc.ServerStreamingServer[pluginv1.ApplyResponse]) error {
	_ = stream.Send(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{Level: pluginv1.TaskEvent_LEVEL_INFO, Message: "applying at the site"}})
	_ = stream.Send(&pluginv1.ApplyResponse{WriteBack: []*pluginv1.ObservedEntity{{
		Kind:         "vm",
		IdentityKeys: map[string]string{"vcenter.uuid": "u42", "dns.fqdn": "vm42.corp"},
	}}})
	return stream.Send(&pluginv1.ApplyResponse{
		Event:  &pluginv1.TaskEvent{Terminal: true, Ok: true},
		Result: &pluginv1.ItemResult{Status: pluginv1.ItemResult_STATUS_CHANGED},
	})
}

// siteLocalClient serves the fake plugin over bufconn and returns its client —
// standing in for the agent's localhost dial to the Site-local plugin.
func siteLocalClient(t *testing.T) pluginv1.PluginServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pluginv1.RegisterPluginServiceServer(srv, sitePlugin{})
	go func() { _ = srv.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(); srv.Stop(); _ = lis.Close() })
	return pluginv1.NewPluginServiceClient(conn)
}

// TestRelay_HostGovernsHubSideOverRelay is the ADR-0049 keystone: pluginhost.Host
// (UNCHANGED) drives a plugin at the far end of the relay, and every governance
// step runs HUB-SIDE over the plugin's raw wire shapes. A community-tier grant must
// gate out the shared dns.fqdn scheme from the relayed write-back exactly as on a
// direct dial — proving the relay forwards opaque bytes and governs nothing (V1).
func TestRelay_HostGovernsHubSideOverRelay(t *testing.T) {
	dialer, acceptor := siterelay.InProcess()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The Site agent: proxies relayed calls to the Site-local plugin. Governs nothing.
	go func() { _ = siterelay.Serve(ctx, acceptor, siteLocalClient(t)) }()

	// The hub: a normal pluginhost.Host, but its client is the relay.
	grant := pluginhost.Grant{
		PluginIdentity:  "vcenter-dev",
		Tier:            pluginhost.TierCommunity,
		Source:          types.Source{Kind: "vcenter", Name: "vcenter-dev"},
		IdentitySchemes: []string{"vcenter.uuid", "dns.fqdn"},
	}
	host := pluginhost.New(nil, siterelay.NewClient(dialer), grant, slog.New(slog.NewTextHandler(io.Discard, nil)))

	raw, err := host.ApplyRaw(ctx, pluginhost.ApplyInvoke{Principal: "alice", Params: []byte(`{}`)})
	if err != nil {
		t.Fatalf("applyRaw over relay: %v", err)
	}
	// Streaming crossed the relay and folded core-side.
	if !raw.Succeeded {
		t.Fatalf("a CHANGED terminal must fold to Succeeded over the relay: %+v", raw)
	}
	if len(raw.WriteBack) != 1 {
		t.Fatalf("want 1 governed write-back entity over the relay, got %d", len(raw.WriteBack))
	}
	// Governance ran HUB-SIDE on the relayed raw bytes: the shared scheme is gated
	// out (community tier), the source-local one survives — identical to a direct dial.
	wb := raw.WriteBack[0]
	if _, leaked := wb.IdentityKeys["dns.fqdn"]; leaked {
		t.Fatalf("the relay must not weaken governance: dns.fqdn leaked through: %+v", wb.IdentityKeys)
	}
	if wb.IdentityKeys["vcenter.uuid"] != "u42" {
		t.Fatalf("source-local identity must survive the relay: %+v", wb.IdentityKeys)
	}
	var gated bool
	for _, r := range raw.Rejections {
		if r.Kind == "identity-scheme" && r.Detail == "dns.fqdn" {
			gated = true
		}
	}
	if !gated {
		t.Fatalf("expected a hub-side dns.fqdn rejection over the relay, got %+v", raw.Rejections)
	}
}
