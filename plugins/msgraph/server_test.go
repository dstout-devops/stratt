package msgraph

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc/metadata"

	"github.com/dstout-devops/stratt/plugins/msgraph/graphsim"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func slogDiscard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// captureStream is a fake gRPC server stream that records the ObserveResponses
// the plugin emits, so the delta protocol can be asserted with no real transport.
type captureStream struct {
	ctx  context.Context
	sent []*pluginv1.ObserveResponse
}

func (c *captureStream) Send(r *pluginv1.ObserveResponse) error {
	c.sent = append(c.sent, r)
	return nil
}
func (c *captureStream) SetHeader(metadata.MD) error  { return nil }
func (c *captureStream) SendHeader(metadata.MD) error { return nil }
func (c *captureStream) SetTrailer(metadata.MD)       {}
func (c *captureStream) Context() context.Context     { return c.ctx }
func (c *captureStream) SendMsg(any) error            { return nil }
func (c *captureStream) RecvMsg(any) error            { return nil }

func simDevice(t *testing.T, sim *httptest.Server, op, id, name string) {
	t.Helper()
	body := fmt.Sprintf(`{"op":%q,"device":{"id":%q,"deviceId":"guid-%s","displayName":%q,"operatingSystem":"Windows","operatingSystemVersion":"11"}}`, op, id, id, name)
	res, err := http.Post(sim.URL+"/_sim/devices", "application/json", bytes.NewBufferString(body))
	if err != nil || res.StatusCode != http.StatusNoContent {
		t.Fatalf("sim mutate: %v %v", res, err)
	}
}

func newSim(t *testing.T) (*graphsim.Sim, *httptest.Server, Config) {
	t.Helper()
	sim := graphsim.New("")
	srv := httptest.NewServer(sim.Handler())
	t.Cleanup(srv.Close)
	sim.SetBase(srv.URL)
	cfg := Config{
		Endpoint: srv.URL + "/v1.0",
		TenantID: "sim", ClientID: "c", ClientSecret: "s",
		TokenURL: srv.URL + "/token",
	}
	return sim, srv, cfg
}

// TestObserveInitialThenDelta proves the plugin's content-expertise and the
// DELTA-cursor protocol in isolation — graphsim over httptest, no core, no
// Postgres. It drives the real code path (OAuth client-credentials → bearer →
// /devices/delta paging) and asserts the two Observe windows the wire carries:
//
//  1. INITIAL (empty cursor): device ObservedEntities across paged nextLinks, a
//     FullSyncComplete boundary, and a non-empty NextCursor (the deltaLink).
//  2. DELTA (that cursor): only the changed devices plus a Gone entry (by the
//     graph.id tombstone scheme) for a removed device, FullSyncComplete=false,
//     and a fresh NextCursor.
//
// (The host side of the wire — cursor persistence, tombstoning — is proven in
// core, so neither module imports the other.)
func TestObserveInitialThenDelta(t *testing.T) {
	_, srv, cfg := newSim(t)
	ctx := context.Background()
	s := NewServer(cfg, slogDiscard())

	for i := 1; i <= 5; i++ {
		simDevice(t, srv, "add", fmt.Sprintf("d%d", i), fmt.Sprintf("DEVICE-%02d", i))
	}

	// (1) INITIAL full enumeration — empty cursor.
	full := &captureStream{ctx: ctx}
	if err := s.Observe(&pluginv1.ObserveRequest{Cursor: ""}, full); err != nil {
		t.Fatalf("initial observe: %v", err)
	}
	if len(full.sent) != 1 {
		t.Fatalf("initial observe: sent %d responses, want 1", len(full.sent))
	}
	r0 := full.sent[0]
	if !r0.GetFullSyncComplete() {
		t.Error("initial observe must set FullSyncComplete=true so the host tombstones absent")
	}
	if r0.GetNextCursor() == "" {
		t.Error("initial observe must carry a NextCursor (the deltaLink)")
	}
	if len(r0.GetGone()) != 0 {
		t.Errorf("initial full pass must carry no Gone entries, got %d", len(r0.GetGone()))
	}
	if len(r0.GetEntities()) != 5 {
		t.Fatalf("initial observe: %d entities, want 5", len(r0.GetEntities()))
	}
	for _, e := range r0.GetEntities() {
		if e.GetKind() != "device" {
			t.Errorf("unexpected kind %q, want device", e.GetKind())
		}
		if e.GetIdentityKeys()["graph.id"] == "" {
			t.Errorf("device missing graph.id identity: %v", e.GetIdentityKeys())
		}
		if len(e.GetFacets()["device.os"]) == 0 {
			t.Errorf("device missing device.os facet blob")
		}
	}
	cursor := r0.GetNextCursor()

	// (2) DELTA window — add, rename, remove.
	simDevice(t, srv, "add", "d6", "DEVICE-06")
	simDevice(t, srv, "update", "d1", "DEVICE-01-RENAMED")
	simDevice(t, srv, "remove", "d2", "")

	delta := &captureStream{ctx: ctx}
	if err := s.Observe(&pluginv1.ObserveRequest{Cursor: cursor}, delta); err != nil {
		t.Fatalf("delta observe: %v", err)
	}
	if len(delta.sent) != 1 {
		t.Fatalf("delta observe: sent %d responses, want 1", len(delta.sent))
	}
	r1 := delta.sent[0]
	if r1.GetFullSyncComplete() {
		t.Error("delta window must set FullSyncComplete=false")
	}
	if r1.GetNextCursor() == "" {
		t.Error("delta window must carry a fresh NextCursor")
	}

	changed := map[string]string{}
	for _, e := range r1.GetEntities() {
		changed[e.GetIdentityKeys()["graph.id"]] = e.GetLabels()["graph.name"]
	}
	if _, ok := changed["d6"]; !ok {
		t.Errorf("delta must carry the added device d6, got %v", changed)
	}
	if changed["d1"] != "DEVICE-01-RENAMED" {
		t.Errorf("delta must carry the rename of d1, got %q", changed["d1"])
	}

	// The removed device surfaces as a Gone entry by the graph.id tombstone scheme.
	var goneD2 bool
	for _, g := range r1.GetGone() {
		if g.GetScheme() == "graph.id" && g.GetValue() == "d2" {
			goneD2 = true
		}
	}
	if !goneD2 {
		t.Errorf("delta must carry a Gone{graph.id=d2} for the removed device, got %v", r1.GetGone())
	}
}

// TestGetManifestSyncer asserts the advertised manifest: SYNCER class, OBSERVE
// verb, POLL observe-mode (Graph delta is poll-based), the device.* facet
// Contracts, and the graph.id tombstone scheme (the plugin's OWN identity scheme).
func TestGetManifestSyncer(t *testing.T) {
	s := NewServer(Config{}, slogDiscard())
	resp, err := s.GetManifest(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	m := resp.GetManifest()
	if m.GetClass().String() != "PLUGIN_CLASS_SYNCER" {
		t.Errorf("class = %s, want PLUGIN_CLASS_SYNCER", m.GetClass())
	}
	if len(m.GetVerbs()) != 1 || m.GetVerbs()[0].String() != "VERB_OBSERVE" {
		t.Errorf("verbs = %v, want [VERB_OBSERVE]", m.GetVerbs())
	}
	if m.GetObserveMode().String() != "OBSERVE_MODE_POLL" {
		t.Errorf("observe mode = %s, want OBSERVE_MODE_POLL", m.GetObserveMode())
	}
	gotContracts := map[string]bool{}
	for _, c := range m.GetContracts() {
		gotContracts[c.GetSchemaId()] = true
	}
	for _, ns := range []string{"device.identity", "device.os", "device.state"} {
		if !gotContracts[ns] {
			t.Errorf("manifest missing contract %s", ns)
		}
	}
	if len(m.GetTombstoneSchemes()) != 1 || m.GetTombstoneSchemes()[0] != "graph.id" {
		t.Errorf("tombstone schemes = %v, want [graph.id]", m.GetTombstoneSchemes())
	}
}

// TestObserveResyncOn410 proves an expired delta token (HTTP 410) degrades to a
// clean full pass in-plugin: the host hands a stale cursor, the plugin re-emits
// the full-sync boundary rather than erroring or losing data.
func TestObserveResyncOn410(t *testing.T) {
	_, srv, cfg := newSim(t)
	ctx := context.Background()
	s := NewServer(cfg, slogDiscard())

	for i := 1; i <= 3; i++ {
		simDevice(t, srv, "add", fmt.Sprintf("d%d", i), fmt.Sprintf("DEVICE-%02d", i))
	}
	full := &captureStream{ctx: ctx}
	if err := s.Observe(&pluginv1.ObserveRequest{Cursor: ""}, full); err != nil {
		t.Fatalf("initial observe: %v", err)
	}
	cursor := full.sent[0].GetNextCursor()

	// Expire every outstanding delta token → the stale cursor now 410s.
	res, err := http.Post(srv.URL+"/_sim/expire", "application/json", nil)
	if err != nil || res.StatusCode != http.StatusNoContent {
		t.Fatalf("sim expire: %v %v", res, err)
	}

	resync := &captureStream{ctx: ctx}
	if err := s.Observe(&pluginv1.ObserveRequest{Cursor: cursor}, resync); err != nil {
		t.Fatalf("resync observe: %v", err)
	}
	r := resync.sent[0]
	if !r.GetFullSyncComplete() {
		t.Error("410 must degrade to a full pass (FullSyncComplete=true)")
	}
	if len(r.GetEntities()) != 3 {
		t.Errorf("resync full pass: %d entities, want 3", len(r.GetEntities()))
	}
	if r.GetNextCursor() == "" {
		t.Error("resync must carry a fresh NextCursor")
	}
}
