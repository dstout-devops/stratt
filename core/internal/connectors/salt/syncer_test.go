package salt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dstout-devops/stratt/core/internal/connectors/salt/saltsim"
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

func minionGrains(id, fqdn, osFamily string) map[string]any {
	return map[string]any{
		"id":            id,
		"fqdn":          fqdn,
		"os":            "CentOS",
		"os_family":     osFamily,
		"osrelease":     "9",
		"osfinger":      "CentOS Stream-9",
		"machine_id":    "mid-" + id,
		"saltversion":   "3008.0",
		"kernel":        "Linux",
		"kernelrelease": "5.14.0-427.el9",
		"kernelversion": "5.14.0",
		"cpuarch":       "x86_64",
		"ipv4":          []any{"127.0.0.1", "10.2.0.30"},
		"ipv6":          []any{"::1", "fe80::2"},
		"fqdn_ip4":      []any{"10.2.0.30"},
	}
}

// TestSaltSyncerProjectsGrains drives the real Syncer against saltsim: the
// runner cache.grains enumeration projects host Entities with salt.node.* facets
// and dns.fqdn identity; a removed minion is tombstoned; a FacetPredicate View on
// os_family resolves (the smart-inventory story, selection via source-scoped
// facets, no labels).
func TestSaltSyncerProjectsGrains(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	sim := saltsim.New()
	sim.SetMinion("web-01.acme.internal", minionGrains("web-01.acme.internal", "web-01.acme.internal", "RedHat"))
	sim.SetMinion("db-01.acme.internal", minionGrains("db-01.acme.internal", "db-01.acme.internal", "RedHat"))
	sim.SetMinion("edge-01.acme.internal", minionGrains("edge-01.acme.internal", "", "Debian")) // no fqdn
	srv := httptest.NewServer(sim.Handler())
	defer srv.Close()

	cfg := Config{APIURL: srv.URL, Username: "stratt", Password: "pw", SourceName: "salt-test"}
	s := NewSyncer(cfg, time.Minute, store, log)
	if err := s.Register(ctx); err != nil {
		t.Fatal(err)
	}
	s.client = newSaltClient(cfg)

	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := store.CountSelector(ctx, types.ViewSelector{Kinds: []string{"host"}}); err != nil || n != 3 {
		t.Fatalf("live hosts: got %d err=%v, want 3", n, err)
	}

	web := findHost(t, store, "web-01.acme.internal")
	if web.IdentityKeys["dns.fqdn"] != "web-01.acme.internal" {
		t.Fatalf("web-01 must carry the fqdn identity, got %v", web.IdentityKeys)
	}
	facets := facetMap(t, store, web.ID)
	assertFacetField(t, facets, "salt.node.identity", "os_family", "RedHat")
	assertFacetField(t, facets, "salt.node.os", "kernelrelease", "5.14.0-427.el9")
	assertFacetField(t, facets, "salt.node.network", "ipv4", "127.0.0.1") // first of the list

	// Smart-inventory View: select RedHat hosts via the source-scoped facet.
	redhat, err := store.ResolveSelector(ctx, types.ViewSelector{
		Kinds:  []string{"host"},
		Facets: []types.FacetPredicate{{Namespace: "salt.node.identity", Path: "os_family", Equals: json.RawMessage(`"RedHat"`)}},
	}, nil, 0)
	if err != nil || len(redhat) != 2 {
		t.Fatalf("RedHat View: got %d err=%v, want 2 (web-01, db-01)", len(redhat), err)
	}

	// Removal → tombstone on the next enumeration.
	sim.RemoveMinion("edge-01.acme.internal")
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := store.CountSelector(ctx, types.ViewSelector{Kinds: []string{"host"}}); err != nil || n != 2 {
		t.Fatalf("after removal: got %d err=%v, want 2", n, err)
	}
}

func findHost(t *testing.T, store *graph.Store, minionID string) types.Entity {
	t.Helper()
	ents, err := store.ResolveSelector(context.Background(), types.ViewSelector{Kinds: []string{"host"}}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.IdentityKeys["salt.minion_id"] == minionID {
			return e
		}
	}
	t.Fatalf("host %s not found", minionID)
	return types.Entity{}
}

func facetMap(t *testing.T, store *graph.Store, entityID string) map[string]types.Facet {
	t.Helper()
	facets, err := store.GetFacets(context.Background(), entityID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]types.Facet{}
	for _, f := range facets {
		out[f.Namespace] = f
	}
	return out
}

func assertFacetField(t *testing.T, facets map[string]types.Facet, ns, field, want string) {
	t.Helper()
	f, ok := facets[ns]
	if !ok {
		t.Fatalf("facet %s not projected", ns)
	}
	doc := map[string]any{}
	if err := json.Unmarshal(f.Value, &doc); err != nil {
		t.Fatalf("unmarshal facet %s: %v", ns, err)
	}
	if got, _ := doc[field].(string); got != want {
		t.Fatalf("facet %s.%s: got %q want %q", ns, field, got, want)
	}
}
