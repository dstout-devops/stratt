package orchestrate

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/pluginhost"
)

// TestSurfaceRejections_NilStoreNoPanic proves the best-effort contract (GOV-3):
// a Run whose daemon has no graph/bus wired still surfaces rejections without
// panicking — the visibility layer is never fatal to the Run.
func TestSurfaceRejections_NilStoreNoPanic(t *testing.T) {
	a := &Activities{} // no Store, no Bus
	a.surfaceRejections(context.Background(), "run-1", "apply", "vcenter",
		[]pluginhost.Rejection{{Kind: "facet", Detail: "ns/foreign", Reason: "outside grant"}})
	// no assertion beyond "did not panic / did not fatal"
	a.surfaceRejections(context.Background(), "run-1", "apply", "vcenter", nil) // empty is a no-op
}

// TestSurfaceRejections_WritesFinding proves a governor rejection becomes a
// tracked, closeable Finding (GOV-3) — not a swallowed log line. DB-gated.
func TestSurfaceRejections_WritesFinding(t *testing.T) {
	store := rejectionTestStore(t)
	a := &Activities{Store: store} // Bus nil: the Finding path is what we assert

	a.surfaceRejections(context.Background(), "run-42", "apply", "opentofu",
		[]pluginhost.Rejection{{Kind: "entity", Detail: "vpc/hijacked", Reason: "land-grab"}})

	fs, err := store.ListFindings(context.Background(), "governance/plugin-rejection", "open", 10)
	if err != nil {
		t.Fatalf("list findings: %v", err)
	}
	if len(fs) == 0 {
		t.Fatal("a governor rejection must produce an open governance/plugin-rejection Finding")
	}

	// Idempotent: the same overreach within the Run folds to one open Finding.
	a.surfaceRejections(context.Background(), "run-42", "apply", "opentofu",
		[]pluginhost.Rejection{{Kind: "entity", Detail: "vpc/hijacked", Reason: "land-grab"}})
	fs2, err := store.ListFindings(context.Background(), "governance/plugin-rejection", "open", 10)
	if err != nil {
		t.Fatalf("list findings (2): %v", err)
	}
	if len(fs2) != len(fs) {
		t.Fatalf("repeated identical rejection must fold to one Finding: had %d, now %d", len(fs), len(fs2))
	}
}

// rejectionTestStore connects to a throwaway migrated database, skipping when no
// Postgres is reachable (CI stands one up — CI-1).
func rejectionTestStore(t *testing.T) *graph.Store {
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
	name := fmt.Sprintf("stratt_gov_rej_%d", os.Getpid())
	_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
		admin.Close()
	})
	base := url
	if i := lastSlash(base); i >= 0 {
		base = base[:i]
	}
	store, err := graph.Connect(ctx, base+"/"+name)
	if err != nil {
		t.Fatalf("connect+migrate: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}
