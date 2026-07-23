package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/dstout-devops/stratt/core/internal/pluginhost"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
	"github.com/dstout-devops/stratt/types"
)

// statestoreResolvePlugin is a real (in-process) statestore provider: its Invoke answers the
// resolve Action with a provider-agnostic s3 backend-config handle, keyed by the workspace the
// core passed — proving the workspace flows through. It asserts the class-level output Contract id.
type statestoreResolvePlugin struct {
	pluginv1.UnimplementedPluginServiceServer
}

func (statestoreResolvePlugin) Invoke(req *pluginv1.InvokeRequest, s grpc.ServerStreamingServer[pluginv1.InvokeResponse]) error {
	var in struct {
		Workspace string `json:"workspace"`
	}
	_ = json.Unmarshal(req.GetArgs().GetBytes(), &in)
	out := map[string]any{"backend": "s3", "config": map[string]string{
		"bucket": "tfstate", "key": "stratt/" + in.Workspace + ".tfstate", "region": "us-east-1", "use_lockfile": "true",
	}}
	raw, _ := json.Marshal(out)
	return s.Send(&pluginv1.InvokeResponse{
		Event:  &pluginv1.TaskEvent{Terminal: true, Ok: true},
		Result: &pluginv1.InvokeResult{Outputs: &pluginv1.Payload{Bytes: raw}, OutputContract: &pluginv1.ContractRef{SchemaId: "capabilities/statestore.output"}},
	})
}

func statestoreResolveClient(t *testing.T) pluginv1.PluginServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pluginv1.RegisterPluginServiceServer(srv, statestoreResolvePlugin{})
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

// TestResolveCapabilitiesEndToEnd drives the ADR-0105 resolve-inject edge through the REAL
// orchestration code against a REAL (in-process) provider plugin: a consumer that requires
// statestore resolves the bound provider's resolve Action, invokes it over the port, reconciles
// the CLASS-level output Contract, validates it, and builds the CapabilityHandle the core injects
// onto the Apply. This is the first exercise of the full chain end-to-end, not per-piece.
func TestResolveCapabilitiesEndToEnd(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	host := pluginhost.New(nil, statestoreResolveClient(t), pluginhost.Grant{Source: types.Source{Name: "s3-statestore"}}, discard)
	plugins := NewPluginRegistry(nil, nil)
	if err := plugins.RegisterAction("s3/statestore-resolve", PluginAction{Host: host}); err != nil {
		t.Fatal(err)
	}
	acts := &Activities{
		Plugins: plugins,
		ResolveCapability: func(_ context.Context, capClass string) (string, error) {
			if capClass == "statestore" {
				return "s3/statestore-resolve", nil
			}
			return "", fmt.Errorf("no verified provider for %q", capClass)
		},
	}

	// The full path: require statestore → resolve the provider action → invoke it → validate the
	// class Contract → build the handle, with the workspace flowing through into the state key.
	handles, err := acts.resolveCapabilities(context.Background(),
		RunInput{Actuator: "opentofu-s3", Params: json.RawMessage(`{"workspace":"web-prod"}`)}, []string{"statestore"})
	if err != nil {
		t.Fatalf("resolveCapabilities: %v", err)
	}
	h, ok := handles["statestore"]
	if !ok {
		t.Fatal("expected a statestore handle")
	}
	if h.Kind != "s3" || h.Config["bucket"] != "tfstate" || h.Config["key"] != "stratt/web-prod.tfstate" || h.Config["use_lockfile"] != "true" {
		t.Fatalf("resolved handle wrong: %+v", h)
	}

	// Fail closed: an empty workspace violates the class INPUT Contract, in the core (guardian Flag B).
	if _, err := acts.resolveCapabilities(context.Background(),
		RunInput{Actuator: "opentofu-s3", Params: json.RawMessage(`{}`)}, []string{"statestore"}); err == nil || !strings.Contains(err.Error(), "input") {
		t.Fatalf("empty workspace must fail the input Contract in the core: %v", err)
	}

	// Fail closed: an unresolvable capability aborts (no silent apply without its backend).
	if _, err := acts.resolveCapabilities(context.Background(),
		RunInput{Actuator: "opentofu-s3", Params: json.RawMessage(`{"workspace":"w"}`)}, []string{"artifactstore"}); err == nil {
		t.Fatal("an unresolvable required capability must fail closed")
	}
}
