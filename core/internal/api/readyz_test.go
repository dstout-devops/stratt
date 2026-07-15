package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dstout-devops/stratt/core/internal/graph"
)

func readyTestStore(t *testing.T) *graph.Store {
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
	name := fmt.Sprintf("stratt_readyz_%d", time.Now().UnixNano())
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
	return store
}

// TestReadyz proves the readiness probe (ADR-0040): 200 when the store is
// reachable (nil bus is skipped), 503 once the store is closed. Distinct from
// the liveness-only /healthz.
func TestReadyz(t *testing.T) {
	store := readyTestStore(t)
	s := &Server{Store: store} // Bus nil → readiness skips the bus check

	rec := httptest.NewRecorder()
	s.handleReadyz(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("ready store: got %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	store.Close()
	rec = httptest.NewRecorder()
	s.handleReadyz(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unreachable store: got %d, want 503", rec.Code)
	}
}

type fakeAuthz struct{ healthErr error }

func (f fakeAuthz) Check(context.Context, string, string, string) (bool, error) { return false, nil }
func (f fakeAuthz) CheckHealth(context.Context) error                           { return f.healthErr }

// TestReadyzAuthzHealth proves /readyz gates on authorization-backend
// reachability (ADR-0040): a healthy authz is 200, an unreachable one is 503.
func TestReadyzAuthzHealth(t *testing.T) {
	store := readyTestStore(t)
	defer store.Close()

	healthy := &Server{Store: store, Authz: fakeAuthz{nil}}
	rec := httptest.NewRecorder()
	healthy.handleReadyz(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthy authz: got %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	down := &Server{Store: store, Authz: fakeAuthz{errors.New("openfga down")}}
	rec = httptest.NewRecorder()
	down.handleReadyz(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unreachable authz: got %d, want 503", rec.Code)
	}
}
