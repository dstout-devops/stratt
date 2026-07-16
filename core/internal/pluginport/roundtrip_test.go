package pluginport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	pluginv1 "github.com/dstout-devops/stratt/core/gen/stratt/plugin/v1"
)

// trivialPlugin is the smallest thing that honors the port: it emits opaque
// payloads only it understands and streams typed TaskEvents. The "core" side of
// the test must route, authorize, provenance, and audit these WITHOUT ever
// unmarshaling a Payload — that is the content-blindness boundary (ADR-0046 §2).
type trivialPlugin struct {
	pluginv1.UnimplementedPluginServiceServer
	applyReceivedDesired []byte // captured to prove Apply carried the opaque bytes through untouched
}

func (p *trivialPlugin) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: &pluginv1.Manifest{
		PluginId:        "trivial",
		ProtocolVersion: "v1",
		Class:           pluginv1.PluginClass_PLUGIN_CLASS_SYNCER,
		Verbs:           []pluginv1.Verb{pluginv1.Verb_VERB_OBSERVE, pluginv1.Verb_VERB_APPLY},
		Contracts:       []*pluginv1.ContractDecl{{SchemaId: "trivial.v1", Sha256: "deadbeef", Band: "S3"}},
	}}, nil
}

func (p *trivialPlugin) Observe(_ *pluginv1.ObserveRequest, stream grpc.ServerStreamingServer[pluginv1.ObserveResponse]) error {
	mk := func(beam string, secret []byte) *pluginv1.Item {
		h := sha256.Sum256(secret)
		return &pluginv1.Item{
			Envelope: &pluginv1.Envelope{
				Coordinates: &pluginv1.Coordinates{Kind: "vm", Band: "S3", Beam: beam},
				Contract:    &pluginv1.ContractRef{SchemaId: "trivial.v1", Sha256: "deadbeef"},
				// The plugin ASSERTS an identity, but the core will not trust it —
				// provenance is stamped from the channel/manifest identity (inv #6).
				Principal:   &pluginv1.Principal{Id: "attacker-claimed", Kind: "user"},
				ContentHash: hex.EncodeToString(h[:]),
			},
			// Payload the core must never parse. Deliberately structured (a) and
			// raw-binary (b) so any accidental JSON parse on the core side would fail.
			Payload: &pluginv1.Payload{Bytes: secret},
		}
	}
	return stream.Send(&pluginv1.ObserveResponse{
		Items: []*pluginv1.Item{
			mk("a", []byte(`{"domain":"only-the-plugin-parses-this"}`)),
			mk("b", []byte{0x00, 0x01, 0x02, 0xff}),
		},
		NextCursor: "done",
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

// TestPortRoundTrip_CoreGovernsEnvelope_NeverPayload is the Phase-A existence
// proof: a real gRPC round-trip where the core governs entirely on the typed
// Envelope and never interprets a Payload. It pins invariants #2, #6, #8, #12
// and the opaque pass-through, matching ADR-0046's Phase-A acceptance ("round-
// trip a trivial in-memory plugin through the full envelope … with no payload
// interpretation by the core").
func TestPortRoundTrip_CoreGovernsEnvelope_NeverPayload(t *testing.T) {
	client, plug := dial(t)
	ctx := context.Background()

	// Invariant #2 — content-blind discovery: identity/capabilities, never "how".
	man, err := client.GetManifest(ctx, &pluginv1.GetManifestRequest{})
	if err != nil {
		t.Fatalf("GetManifest: %v", err)
	}
	// This stands in for the authenticated-channel identity (inv #3). In a real
	// deployment it is the mTLS/token principal, not the manifest string; the
	// point under test is that provenance derives from the CHANNEL, not the payload.
	channelIdentity := man.GetManifest().GetPluginId()
	if channelIdentity != "trivial" {
		t.Fatalf("manifest identity = %q, want trivial", channelIdentity)
	}

	type prov struct{ source, kind, band string }
	stamped := map[string]prov{}
	var audit []string

	// Observe: the core reads envelopes only.
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
		for _, it := range resp.GetItems() {
			env := it.GetEnvelope()
			co := env.GetCoordinates()

			// Invariant #8 — content-blind change detection: the core HASHES the
			// opaque bytes (never parses their structure) and checks the plugin's
			// asserted content_hash. Hashing is blind; unmarshaling would not be.
			h := sha256.Sum256(it.GetPayload().GetBytes())
			if got := hex.EncodeToString(h[:]); got != env.GetContentHash() {
				t.Fatalf("content_hash mismatch for beam %q: got %s want %s", co.GetBeam(), got, env.GetContentHash())
			}

			// Invariant #6 — provenance stamped from the CHANNEL identity, never
			// from the payload and never from the plugin's envelope Principal claim
			// (which is deliberately "attacker-claimed" here).
			stamped[co.GetBeam()] = prov{source: channelIdentity, kind: co.GetKind(), band: co.GetBand()}
			audit = append(audit, "observe:"+co.GetKind()+"/"+co.GetBeam())
		}
	}

	if len(stamped) != 2 {
		t.Fatalf("expected 2 observed items, got %d", len(stamped))
	}
	for beam, p := range stamped {
		if p.source != "trivial" {
			t.Fatalf("beam %q: provenance source must be the channel identity, got %q (payload/claim leaked in)", beam, p.source)
		}
		if p.kind != "vm" || p.band != "S3" {
			t.Fatalf("beam %q: routed on wrong coordinates: %+v", beam, p)
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
		// Invariant #12 — the descent/diagnostic stream is typed and legible, not
		// an opaque blob; the core can read and audit it.
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
