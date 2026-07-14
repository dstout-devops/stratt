package graph

import (
	"context"
	"fmt"
	neturl "net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// freshDBURL creates a throwaway, un-migrated database and returns its URL + a
// cleanup — distinct from testStore, which also migrates (this test needs to run
// the migration itself, concurrently).
func freshDBURL(t *testing.T) (string, func()) {
	t.Helper()
	base := os.Getenv("STRATT_TEST_DATABASE_URL")
	if base == "" {
		base = "postgres://stratt:stratt-dev@localhost:5432/stratt"
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Skipf("no test database reachable (%v) — run `task dev:up`", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Skipf("no test database reachable (%v) — run `task dev:up`", err)
	}
	name := fmt.Sprintf("stratt_ha_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatalf("create db: %v", err)
	}
	u, err := neturl.Parse(base)
	if err != nil {
		t.Fatalf("parse database url: %v", err)
	}
	u.Path = "/" + name
	return u.String(), func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
		admin.Close()
	}
}

// TestMigrateConcurrent proves the goose Postgres session-lock (ADR-0040): N
// replicas racing Connect() (which migrates at boot) against one fresh database
// all succeed — the advisory lock serializes Up(), no partial-apply error.
func TestMigrateConcurrent(t *testing.T) {
	url, cleanup := freshDBURL(t)
	defer cleanup()

	const replicas = 4
	var wg sync.WaitGroup
	errs := make([]error, replicas)
	stores := make([]*Store, replicas)
	for i := 0; i < replicas; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := Connect(context.Background(), url)
			errs[i], stores[i] = err, s
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("replica %d Connect failed (migration race not serialized): %v", i, err)
		}
	}
	for _, s := range stores {
		if s != nil {
			s.Close()
		}
	}
}

// TestStorePing proves the readiness signal: a live store pings clean; a closed
// pool errors (so /readyz would report 503).
func TestStorePing(t *testing.T) {
	url, cleanup := freshDBURL(t)
	defer cleanup()
	s, err := Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Ping(context.Background()); err != nil {
		t.Fatalf("live store should ping clean: %v", err)
	}
	s.Close()
	if err := s.Ping(context.Background()); err == nil {
		t.Fatal("closed store must fail ping (readiness → 503)")
	}
}
