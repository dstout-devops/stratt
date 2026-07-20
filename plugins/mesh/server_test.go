package mesh

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"google.golang.org/grpc"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

type fakeSource struct {
	edges []TrafficEdge
	err   error
}

func (f fakeSource) Edges(context.Context) ([]TrafficEdge, error) { return f.edges, f.err }

// captureStream is a minimal ServerStreamingServer that records sent responses.
type captureStream struct {
	grpc.ServerStream
	ctx  context.Context
	sent []*pluginv1.ObserveResponse
}

func (c *captureStream) Context() context.Context { return c.ctx }
func (c *captureStream) Send(r *pluginv1.ObserveResponse) error {
	c.sent = append(c.sent, r)
	return nil
}
func (c *captureStream) RecvMsg(any) error { return io.EOF }

func TestManifest_SyncerNoFacetTombstoneFQDN(t *testing.T) {
	s := NewServer(Config{}, fakeSource{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	resp, err := s.GetManifest(context.Background(), &pluginv1.GetManifestRequest{})
	if err != nil {
		t.Fatal(err)
	}
	m := resp.GetManifest()
	if m.GetClass() != pluginv1.PluginClass_PLUGIN_CLASS_SYNCER {
		t.Fatalf("class must be SYNCER, got %v", m.GetClass())
	}
	if len(m.GetContracts()) != 0 {
		t.Fatalf("the mesh declares NO Contract (identity + identity-only edges), got %v", m.GetContracts())
	}
	if got := m.GetTombstoneSchemes(); len(got) != 1 || got[0] != SchemeFQDN {
		t.Fatalf("tombstone scheme must be the shared dns.fqdn, got %v", got)
	}
	if m.GetPluginId() != "mesh" {
		t.Fatalf("default plugin id must be `mesh`, got %q", m.GetPluginId())
	}
}

func TestObserve_FullSyncEmitsAnchorsAndEdges(t *testing.T) {
	s := NewServer(Config{}, fakeSource{edges: []TrafficEdge{
		{FromFQDN: "web.p.svc", ToFQDN: "api.p.svc"},
	}}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	stream := &captureStream{ctx: context.Background()}
	if err := s.Observe(&pluginv1.ObserveRequest{}, stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.sent) != 1 {
		t.Fatalf("expected one full-sync response, got %d", len(stream.sent))
	}
	resp := stream.sent[0]
	if !resp.GetFullSyncComplete() {
		t.Fatal("the response must carry the full_sync_complete boundary (drives the presence sweep)")
	}
	if len(resp.GetEntities()) != 2 {
		t.Fatalf("expected web + api anchors, got %d", len(resp.GetEntities()))
	}
}

// TestObserve_EmptySnapshotHoldsSteady is the guardian §1.8 guardrail: an empty result
// vector (most often a query/label misconfiguration against a live mesh) must NOT emit a
// full-sync boundary — that would drive the host's relation-presence GC to retract every
// mesh-asserted dependency. The cycle holds steady (FullSyncComplete=false ⇒ no GC).
func TestObserve_EmptySnapshotHoldsSteady(t *testing.T) {
	s := NewServer(Config{}, fakeSource{edges: nil}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	stream := &captureStream{ctx: context.Background()}
	if err := s.Observe(&pluginv1.ObserveRequest{}, stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.sent) != 1 {
		t.Fatalf("expected one response, got %d", len(stream.sent))
	}
	if stream.sent[0].GetFullSyncComplete() {
		t.Fatal("an empty snapshot must NOT assert full_sync_complete (else it mass-retracts every edge)")
	}
	if len(stream.sent[0].GetEntities()) != 0 {
		t.Fatal("no anchors to emit on an empty snapshot")
	}
}

// TestObserve_EmptySnapshotAllowed: an operator who runs a mesh legitimately expected to
// be idle opts in, and the empty full sync IS asserted (so a real drain-to-zero collects).
func TestObserve_EmptySnapshotAllowed(t *testing.T) {
	s := NewServer(Config{AllowEmptyFullSync: true}, fakeSource{edges: nil}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	stream := &captureStream{ctx: context.Background()}
	if err := s.Observe(&pluginv1.ObserveRequest{}, stream); err != nil {
		t.Fatal(err)
	}
	if !stream.sent[0].GetFullSyncComplete() {
		t.Fatal("with AllowEmptyFullSync, an empty snapshot must assert the full sync (a real drain-to-zero collects)")
	}
}

// TestObserve_SourceError: a telemetry-backend failure surfaces as an Observe error
// (NOT an empty full sync, which would collect every dependency, §1.8 never hide failure).
func TestObserve_SourceError(t *testing.T) {
	s := NewServer(Config{}, fakeSource{err: context.DeadlineExceeded}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	stream := &captureStream{ctx: context.Background()}
	if err := s.Observe(&pluginv1.ObserveRequest{}, stream); err == nil {
		t.Fatal("a telemetry error must surface, never emit an empty (all-collect) full sync")
	}
	if len(stream.sent) != 0 {
		t.Fatal("no response may be sent on a source error")
	}
}
