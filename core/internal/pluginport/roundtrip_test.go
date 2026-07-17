package pluginport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// trivialPlugin is the smallest thing that honors the port: it emits core-legible
// ObservedEntities whose facet VALUES are blobs only it understands, and streams
// typed TaskEvents. The "core" side must route/own/provenance/audit these from
// the Envelope/structure — and never interpret a facet value or an Apply payload.
type trivialPlugin struct {
	pluginv1.UnimplementedPluginServiceServer
	applyReceivedDesired []byte // captured to prove Apply carried the opaque bytes through untouched
}

func (p *trivialPlugin) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:         "trivial",
		ProtocolVersion:  "v1",
		Class:            pluginv1.PluginClass_PLUGIN_CLASS_SYNCER,
		Verbs:            []pluginv1.Verb{pluginv1.Verb_VERB_OBSERVE, pluginv1.Verb_VERB_APPLY},
		Contracts:        []*pluginv1.ContractDecl{{SchemaId: "vm.config", Sha256: "deadbeef", Band: "S3"}},
		TombstoneSchemes: []string{"trivial.id"},
	}}, nil
}

func (p *trivialPlugin) Observe(_ *pluginv1.ObserveRequest, stream grpc.ServerStreamingServer[pluginv1.ObserveResponse]) error {
	mk := func(id string, facetVal []byte) *pluginv1.ObservedEntity {
		return &pluginv1.ObservedEntity{
			Kind:         "vm",
			IdentityKeys: map[string]string{"trivial.id": id},
			Labels:       map[string]string{"trivial.name": id},
			// Facet value is a blob the core validates against a pinned schema but
			// never structure-parses. (a) valid JSON, (b) raw binary — a naive
			// json.Unmarshal on the core side would choke on (b), proving opacity.
			Facets: map[string][]byte{"vm.config": facetVal},
		}
	}
	return stream.Send(&pluginv1.ObserveResponse{
		Entities: []*pluginv1.ObservedEntity{
			mk("a", []byte(`{"cpus":2}`)),
			mk("b", []byte{0x00, 0x01, 0x02, 0xff}),
		},
		FullSyncComplete: true,
	})
}

func (p *trivialPlugin) Apply(req *pluginv1.ApplyRequest, stream grpc.ServerStreamingServer[pluginv1.ApplyResponse]) error {
	p.applyReceivedDesired = req.GetDesired().GetBytes()
	_ = stream.Send(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_INFO, Message: "applying",
	}})
	return stream.Send(&pluginv1.ApplyResponse{Event: &pluginv1.TaskEvent{
		Level: pluginv1.TaskEvent_LEVEL_INFO, Message: "converged", Terminal: true, Ok: true,
	}})
}

func dial(t *testing.T) (pluginv1.PluginServiceClient, *trivialPlugin) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	plug := &trivialPlugin{}
	pluginv1.RegisterPluginServiceServer(srv, plug)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(); srv.Stop(); _ = lis.Close() })
	return pluginv1.NewPluginServiceClient(conn), plug
}

// TestPortRoundTrip_CoreGovernsStructure_NeverContent is the port-mechanics
// proof: a real gRPC round-trip where the core governs on the legible
// structure/envelope and never interprets a facet value or an Apply payload. It
// pins invariants #2, #6, #12 and the opaque Apply pass-through. (The full
// grant/authz/provenance-to-graph path is proven against Postgres+vcsim in the
// host integration test.)
func TestPortRoundTrip_CoreGovernsStructure_NeverContent(t *testing.T) {
	client, plug := dial(t)
	ctx := context.Background()

	// Invariant #2 — content-blind discovery.
	man, err := client.GetManifest(ctx, &pluginv1.GetManifestRequest{})
	if err != nil {
		t.Fatalf("GetManifest: %v", err)
	}
	// Stands in for the authenticated-channel identity (inv #3): provenance
	// derives from the channel, never from the payload.
	channelIdentity := man.GetManifest().GetPluginId()
	if channelIdentity != "trivial" {
		t.Fatalf("manifest identity = %q, want trivial", channelIdentity)
	}

	type prov struct{ source, kind string }
	stamped := map[string]prov{}
	var audit []string
	var sawFullSync bool

	os, err := client.Observe(ctx, &pluginv1.ObserveRequest{})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	for {
		resp, err := os.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Observe recv: %v", err)
		}
		for _, e := range resp.GetEntities() {
			// The core reads kind/identity/labels LEGIBLY. There is NO principal
			// on an ObservedEntity, so the plugin cannot claim provenance — the
			// host stamps it from the channel identity (invariant #6, structural).
			id := e.GetIdentityKeys()["trivial.id"]
			stamped[id] = prov{source: channelIdentity, kind: e.GetKind()}
			audit = append(audit, "observe:"+e.GetKind()+"/"+id)

			// The facet VALUE is treated as an opaque blob — never structure-parsed.
			// Prove it: entity "b" carries invalid JSON, so a json.Unmarshal here
			// would error; the core must not do that on the value.
			if len(e.GetFacets()["vm.config"]) == 0 {
				t.Fatalf("entity %q missing facet blob", id)
			}
			if json.Valid(e.GetFacets()["vm.config"]) && id == "b" {
				t.Fatalf("entity b was expected to be non-JSON binary; opacity test is moot")
			}
		}
		if resp.GetFullSyncComplete() {
			sawFullSync = true
		}
	}

	if len(stamped) != 2 {
		t.Fatalf("expected 2 observed entities, got %d", len(stamped))
	}
	if !sawFullSync {
		t.Fatal("Observe must signal full_sync_complete so the host can tombstone (ADR-0042)")
	}
	for id, p := range stamped {
		if p.source != "trivial" || p.kind != "vm" {
			t.Fatalf("entity %q: bad governance %+v (provenance must be channel-derived)", id, p)
		}
	}

	// Apply: the core hands an OPAQUE desired blob through; the plugin receives
	// it byte-identical. The core never built it by interpreting content.
	desired := []byte{0xde, 0xad, 0xbe, 0xef, ' ', 'o', 'p', 'a', 'q', 'u', 'e'}
	as, err := client.Apply(ctx, &pluginv1.ApplyRequest{
		Envelope: &pluginv1.Envelope{
			Coordinates: &pluginv1.Coordinates{Kind: "vm"},
			Principal:   &pluginv1.Principal{Id: "core", Kind: "service"},
		},
		Desired: &pluginv1.Payload{Bytes: desired},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var sawTerminalOK bool
	for {
		resp, err := as.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Apply recv: %v", err)
		}
		ev := resp.GetEvent()
		// Invariant #12 — the descent/diagnostic stream is typed and legible.
		audit = append(audit, "apply:"+ev.GetMessage())
		if ev.GetTerminal() {
			sawTerminalOK = ev.GetOk()
		}
	}
	if !sawTerminalOK {
		t.Fatal("Apply must end with a terminal TaskEvent carrying ok=true")
	}
	if string(plug.applyReceivedDesired) != string(desired) {
		t.Fatalf("opaque desired payload must cross the wire byte-identical: got %x want %x", plug.applyReceivedDesired, desired)
	}
	if len(audit) < 4 { // 2 observe + >=2 apply
		t.Fatalf("audit stream must capture observe+apply on the one seam, got %v", audit)
	}
}
