package msgraph

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dstout-devops/stratt/core/internal/connectors/msgraph/graphsim"
	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

// testStore mirrors the repo's integration-test helper: throwaway database,
// migrations applied, skip when no substrate reachable.
func testStore(t *testing.T) *graph.Store {
	t.Helper()
	url := os.Getenv("STRATT_TEST_DATABASE_URL")
	if url == "" {
		url = "postgres://stratt:stratt-dev@localhost:5432/stratt"
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("no test database reachable (%v) — run `task dev:up`", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Skipf("no test database reachable (%v) — run `task dev:up`", err)
	}
	name := fmt.Sprintf("stratt_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
		admin.Close()
	})
	u, err := neturl.Parse(url)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	store, err := graph.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("connect+migrate test database: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

func simDevice(sim *httptest.Server, t *testing.T, op, id, name string) {
	t.Helper()
	body := fmt.Sprintf(`{"op":%q,"device":{"id":%q,"deviceId":"guid-%s","displayName":%q,"operatingSystem":"Windows","operatingSystemVersion":"11"}}`, op, id, id, name)
	res, err := http.Post(sim.URL+"/_sim/devices", "application/json", bytes.NewBufferString(body))
	if err != nil || res.StatusCode != http.StatusNoContent {
		t.Fatalf("sim mutate: %v %v", res, err)
	}
}

// TestSyncerDeltaLifecycle drives the real Syncer against the sim through
// the full protocol: paged full enumeration, incremental delta, rename,
// removal→tombstone, cursor persistence across a fresh Syncer (restart),
// and token expiry (410) → clean full resync.
func TestSyncerDeltaLifecycle(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	sim := graphsim.New("")
	srv := httptest.NewServer(sim.Handler())
	defer srv.Close()
	sim.SetBase(srv.URL)

	for i := 1; i <= 5; i++ {
		simDevice(srv, t, "add", fmt.Sprintf("d%d", i), fmt.Sprintf("DEVICE-%02d", i))
	}

	cfg := Config{
		Endpoint: srv.URL + "/v1.0", TenantID: "sim", ClientID: "c", ClientSecret: "s",
		TokenURL: srv.URL + "/token", SourceName: "graph-test",
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	s := NewSyncer(cfg, time.Second, store, log)
	if err := s.Register(ctx); err != nil {
		t.Fatal(err)
	}
	s.client = cfg.httpClient(ctx)

	// Full enumeration (3 pages at sim page size 2).
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	assertLive(t, store, 5)

	// Incremental: add + rename + remove, one delta cycle.
	simDevice(srv, t, "add", "d6", "DEVICE-06")
	simDevice(srv, t, "update", "d1", "DEVICE-01-RENAMED")
	simDevice(srv, t, "remove", "d2", "")
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	assertLive(t, store, 5) // 5 + 1 - 1
	if name := labelOf(t, store, "d1"); name != "DEVICE-01-RENAMED" {
		t.Fatalf("rename must flow through delta, got %q", name)
	}

	// Restart: a fresh Syncer resumes from the stored cursor (delta, not
	// full) — prove it by checking the cursor is non-empty and unchanged
	// semantics: a no-op cycle projects nothing new.
	s2 := NewSyncer(cfg, time.Second, store, log)
	if err := s2.Register(ctx); err != nil {
		t.Fatal(err)
	}
	s2.client = cfg.httpClient(ctx)
	cur, err := store.SyncCursor(ctx, s2.source.ID)
	if err != nil || cur == "" {
		t.Fatalf("cursor must persist across restarts: %q %v", cur, err)
	}
	if err := s2.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	assertLive(t, store, 5)

	// Expired token → 410 → errResync → cleared cursor → full resync.
	res, err := http.Post(srv.URL+"/_sim/expire", "application/json", nil)
	if err != nil || res.StatusCode != http.StatusNoContent {
		t.Fatalf("sim expire: %v %v", res, err)
	}
	err = s2.Sync(ctx)
	if !errors.Is(err, errResync) {
		t.Fatalf("expired token must surface as resync, got %v", err)
	}
	if err := store.SetSyncCursor(ctx, s2.source.ID, "", true); err != nil { // what Run does
		t.Fatal(err)
	}
	if err := s2.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	assertLive(t, store, 5)
}

func assertLive(t *testing.T, store *graph.Store, want int) {
	t.Helper()
	n, err := store.CountSelector(context.Background(), types.ViewSelector{Kinds: []string{"device"}})
	if err != nil {
		t.Fatal(err)
	}
	if int(n) != want {
		t.Fatalf("live devices: got %d want %d", n, want)
	}
}

// labelOf finds the device entity carrying identity graph.id=id and returns
// its graph.name label.
func labelOf(t *testing.T, store *graph.Store, id string) string {
	t.Helper()
	ents, err := store.ResolveSelector(context.Background(), types.ViewSelector{Kinds: []string{"device"}}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.IdentityKeys["graph.id"] == id {
			return e.Labels["graph.name"]
		}
	}
	t.Fatalf("device %s not found", id)
	return ""
}
