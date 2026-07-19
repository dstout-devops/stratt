package graph

import (
	"context"
	"fmt"
	neturl "net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestConnectNoMigrateAndMigrateURL proves the UPG-1 controlled-migration seams:
// ConnectNoMigrate connects WITHOUT applying migrations (a serving replica during
// a rolling upgrade), and MigrateURL applies them (the pre-upgrade Job). DB-gated.
func TestConnectNoMigrateAndMigrateURL(t *testing.T) {
	url := os.Getenv("STRATT_TEST_DATABASE_URL")
	if url == "" {
		url = "postgres://stratt:stratt-dev@localhost:5432/stratt"
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("no test database reachable (%v)", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Skipf("no test database reachable (%v)", err)
	}
	defer admin.Close()

	name := fmt.Sprintf("stratt_upg_test_%d", os.Getpid())
	_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create db: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)") })

	u, _ := neturl.Parse(url)
	u.Path = "/" + name
	dbURL := u.String()

	// ConnectNoMigrate: connects but does NOT migrate — the audit chain table is absent.
	s, err := ConnectNoMigrate(ctx, dbURL)
	if err != nil {
		t.Fatalf("ConnectNoMigrate: %v", err)
	}
	if got := tableExists(t, s.pool, "audit", "event"); got {
		t.Fatal("ConnectNoMigrate must NOT create schema; audit.event should be absent")
	}
	s.Close()

	// MigrateURL: applies the schema — now the table exists.
	if err := MigrateURL(ctx, dbURL); err != nil {
		t.Fatalf("MigrateURL: %v", err)
	}
	s2, err := ConnectNoMigrate(ctx, dbURL)
	if err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	defer s2.Close()
	if got := tableExists(t, s2.pool, "audit", "event"); !got {
		t.Fatal("MigrateURL must apply the schema; audit.event should exist")
	}
}

func tableExists(t *testing.T, pool *pgxpool.Pool, schema, table string) bool {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.tables WHERE table_schema=$1 AND table_name=$2`,
		schema, table).Scan(&n)
	if err != nil {
		t.Fatalf("table check: %v", err)
	}
	return n == 1
}
