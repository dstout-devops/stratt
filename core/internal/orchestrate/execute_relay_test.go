package orchestrate

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"

	"go.temporal.io/sdk/testsuite"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/dstout-devops/stratt/core/internal/dispatch"
	"github.com/dstout-devops/stratt/core/internal/pluginhost"
	"github.com/dstout-devops/stratt/core/internal/siterelay"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
	"github.com/dstout-devops/stratt/types"
)

// relaySitePlugin is a plugin "at the Site": Apply streams a write-back with a
// granted + an ungranted-for-community scheme, then a terminal CHANGED. manifestID
// is what its GetManifest asserts (the hub validates it against the grant, F1).
type relaySitePlugin struct {
	pluginv1.UnimplementedPluginServiceServer
	manifestID string
}

func (p relaySitePlugin) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	id := p.manifestID
	if id == "" {
		id = "vcenter-dev"
	}
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId: id, ProtocolVersion: "v1", Class: pluginv1.PluginClass_PLUGIN_CLASS_ACTUATOR,
	}}, nil
}

func (relaySitePlugin) Apply(_ *pluginv1.ApplyRequest, s grpc.ServerStreamingServer[pluginv1.ApplyResponse]) error {
	_ = s.Send(&pluginv1.ApplyResponse{WriteBack: []*pluginv1.ObservedEntity{{
		Kind: "vm", IdentityKeys: map[string]string{"vcenter.uuid": "u7", "dns.fqdn": "vm7.corp"},
	}}})
	return s.Send(&pluginv1.ApplyResponse{
		Event: &pluginv1.TaskEvent{Terminal: true, Ok: true}, Result: &pluginv1.ItemResult{Status: pluginv1.ItemResult_STATUS_CHANGED},
	})
}

func relaySiteClient(t *testing.T, manifestID string) pluginv1.PluginServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pluginv1.RegisterPluginServiceServer(srv, relaySitePlugin{manifestID: manifestID})
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

// TestExecutePlugin_RemoteSiteViaRelay proves ADR-0049 slice 3: Execute routes a
// remote-Site plugin Step through the relay (no hub-only guard), and governance
// runs HUB-SIDE from the hub-held grant — the community-tier shared dns.fqdn is
// gated out of the relayed write-back, the source-local vcenter.uuid survives.
func TestExecutePlugin_RemoteSiteViaRelay(t *testing.T) {
	dialer, acceptor := siterelay.InProcess()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = siterelay.Serve(ctx, acceptor, relaySiteClient(t, "vcenter-dev")) }()

	grant := pluginhost.Grant{
		PluginIdentity: "vcenter-dev", Tier: pluginhost.TierCommunity,
		Source: types.Source{Kind: "vcenter", Name: "vcenter-dev"}, IdentitySchemes: []string{"vcenter.uuid", "dns.fqdn"},
	}
	a := &Activities{
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		RelayDial: func(string, string) siterelay.Dialer { return dialer },
		Plugins: NewPluginRegistryWith(map[string]PluginActuator{
			"tofu": {Grant: grant, DryRunnable: true}, // no hub-local Host — Site-only
		}, nil),
	}
	// Execute uses activity.RecordHeartbeat/GetLogger → run it in a Temporal test
	// activity env (the established orchestrate-test pattern).
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestActivityEnvironment()
	env.RegisterActivity(a.Execute)
	val, err := env.ExecuteActivity(a.Execute, RunInput{Actuator: "tofu", Principal: "alice"}, 0, "edge-1", ResolvedTargets{}, []dispatch.CredentialMount(nil))
	if err != nil {
		t.Fatalf("execute over relay: %v", err)
	}
	var res dispatch.Result
	if err := val.Get(&res); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !res.Succeeded || len(res.Entities) != 1 {
		t.Fatalf("relay Apply must fold Succeeded + surface governed write-back: %+v", res)
	}
	if _, leaked := res.Entities[0].IdentityKeys["dns.fqdn"]; leaked {
		t.Fatal("governance must run hub-side over the relay: dns.fqdn leaked through a remote-Site Apply")
	}
	if res.Entities[0].IdentityKeys["vcenter.uuid"] != "u7" {
		t.Fatalf("source-local identity must survive the relay: %+v", res.Entities[0].IdentityKeys)
	}
}

// TestExecutePlugin_RemoteSiteNoRelayFailsVisibly proves a remote-Site plugin Step
// with no relay configured fails VISIBLY, never silently hub-local (§1.8).
func TestExecutePlugin_RemoteSiteNoRelayFailsVisibly(t *testing.T) {
	a := &Activities{Plugins: NewPluginRegistryWith(map[string]PluginActuator{"tofu": {DryRunnable: true}}, nil)}
	_, err := a.Execute(context.Background(), RunInput{Actuator: "tofu"}, 0, "edge-1", ResolvedTargets{}, nil)
	if err == nil || !strings.Contains(err.Error(), "no plugin relay is configured") {
		t.Fatalf("remote-Site plugin with no relay must fail visibly, got %v", err)
	}
}

// TestExecutePlugin_SiteManifestMismatchRejected proves ADR-0049 F1: a Site plugin
// whose relayed Manifest asserts an identity ≠ the hub-held grant is REJECTED
// hub-side before any verb runs — a compromised agent cannot relay a different
// plugin under the grant's authority.
func TestExecutePlugin_SiteManifestMismatchRejected(t *testing.T) {
	dialer, acceptor := siterelay.InProcess()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The plugin at the Site asserts the WRONG identity.
	go func() { _ = siterelay.Serve(ctx, acceptor, relaySiteClient(t, "attacker-plugin")) }()

	grant := pluginhost.Grant{PluginIdentity: "vcenter-dev", Tier: pluginhost.TierTrusted,
		Source: types.Source{Kind: "vcenter", Name: "vcenter-dev"}, IdentitySchemes: []string{"vcenter.uuid"}}
	a := &Activities{
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		RelayDial: func(string, string) siterelay.Dialer { return dialer },
		Plugins:   NewPluginRegistryWith(map[string]PluginActuator{"tofu": {Grant: grant, DryRunnable: true}}, nil),
	}
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestActivityEnvironment()
	env.RegisterActivity(a.Execute)
	_, err := env.ExecuteActivity(a.Execute, RunInput{Actuator: "tofu", Principal: "alice"}, 0, "edge-1", ResolvedTargets{}, []dispatch.CredentialMount(nil))
	if err == nil || !strings.Contains(err.Error(), "anti-spoof") {
		t.Fatalf("a Site plugin whose manifest ≠ grant must be rejected hub-side (F1), got %v", err)
	}
}
