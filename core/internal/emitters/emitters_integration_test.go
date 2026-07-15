package emitters

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

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
	name := fmt.Sprintf("stratt_emit_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatalf("create db: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
		admin.Close()
	})
	u, _ := neturl.Parse(url)
	u.Path = "/" + name
	store, err := graph.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("connect+migrate: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

// TestStreamEmitterIngestRejected proves (a) a token-less stream Emitter persists
// (migration 00021 widened the kind CHECK) and (b) an inbound POST to it is
// rejected 400 — a stream subscriber is outbound, not an ingest endpoint (ADR-0039).
func TestStreamEmitterIngestRejected(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.UpsertEmitter(ctx, types.Emitter{Name: "salt", Kind: types.EmitterStream}); err != nil {
		t.Fatalf("upsert stream emitter (CHECK widened?): %v", err)
	}

	h := New(store, nil, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	srv := httptest.NewServer(h)
	defer srv.Close()

	res, err := http.Post(srv.URL+"/emitters/salt", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST to a stream emitter must be 400, got %s", res.Status)
	}
}
