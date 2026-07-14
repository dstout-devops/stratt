package chef

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

	"github.com/dstout-devops/stratt/core/internal/connectors/chef/chefsim"
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

func linuxNode(name, env, fqdn string, roles ...string) chefsim.Node {
	auto := map[string]any{
		"platform":         "ubuntu",
		"platform_family":  "debian",
		"platform_version": "22.04",
		"os":               "linux",
		"ipaddress":        "10.0.0.10",
		"macaddress":       "00:11:22:33:44:55",
		"kernel":           map[string]any{"name": "Linux", "release": "5.15.0-91-generic", "machine": "x86_64"},
		"chef_packages":    map[string]any{"chef": map[string]any{"version": "15.17.4"}},
	}
	if fqdn != "" {
		auto["fqdn"] = fqdn
	}
	runList := make([]string, 0, len(roles))
	for _, r := range roles {
		runList = append(runList, "role["+r+"]")
	}
	return chefsim.Node{Name: name, Environment: env, RunList: runList, Automatic: auto}
}

// TestChefSyncerProjectsAndCorrelates drives the real Syncer against the
// signature-verifying sim: full enumeration projects host Entities with facets
// and smart-inventory labels; a node sharing dns.fqdn MERGES with a host already
// in the graph (cross-source correlation, §1.2); a removed node is tombstoned.
func TestChefSyncerProjectsAndCorrelates(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	key, keyPEM, err := chefsim.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sim := chefsim.New("acme", "stratt", key)
	sim.Set(linuxNode("web-01", "production", "web-01.acme.internal", "web"))
	sim.Set(linuxNode("db-01", "production", "", "database")) // no fqdn → name-only identity
	sim.Set(linuxNode("cache-01", "staging", "cache-01.acme.internal", "cache"))
	srv := httptest.NewServer(sim.Handler())
	defer srv.Close()

	// Another Source already observed web-01 by its fqdn — Chef must correlate
	// onto it, not create a second host Entity.
	seedSrc, err := store.RegisterSource(ctx, types.Source{Kind: "seed", Name: "seed"})
	if err != nil {
		t.Fatal(err)
	}
	seedProv := types.Provenance{WriterKind: types.WriterSyncer, WriterRef: "connector/seed/seed/syncer", SourceID: seedSrc.ID, At: time.Now().UTC()}
	if _, err := store.NormalizerProjector().UpsertEntities(ctx, seedProv, []graph.EntityUpsert{{
		Kind:         "host",
		IdentityKeys: map[string]string{"dns.fqdn": "web-01.acme.internal"},
		Labels:       map[string]string{"seed": "yes"},
	}}); err != nil {
		t.Fatal(err)
	}

	cfg := Config{ServerURL: srv.URL + "/organizations/acme/", ClientName: "stratt", KeyPEM: keyPEM, SourceName: "acme-chef"}
	s := NewSyncer(cfg, time.Minute, store, log)
	if err := s.Register(ctx); err != nil {
		t.Fatal(err)
	}
	client, err := cfg.chefClient()
	if err != nil {
		t.Fatal(err)
	}
	s.client = client

	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	// 3 hosts, not 4 — web-01 merged with the pre-seeded fqdn entity.
	if n, err := store.CountSelector(ctx, types.ViewSelector{Kinds: []string{"host"}}); err != nil || n != 3 {
		t.Fatalf("live hosts: got %d err=%v, want 3 (web-01 must merge, not duplicate)", n, err)
	}

	// The chef.node.name entity also carries the fqdn identity it correlated
	// onto: the count==3 above already proves it merged with the pre-seeded host
	// rather than duplicating it. (Labels are a whole-set last-writer projection,
	// so Chef's label set — not the seed's — is what remains; see ADR-0037.)
	web := findHost(t, store, "web-01")
	if web.IdentityKeys["dns.fqdn"] != "web-01.acme.internal" {
		t.Fatalf("web-01 must carry the fqdn identity, got %v", web.IdentityKeys)
	}
	if web.Labels["chef.environment"] != "production" || web.Labels["chef.role.web"] != "true" {
		t.Fatalf("smart-inventory labels missing: %v", web.Labels)
	}

	facets := facetMap(t, store, web.ID)
	assertFacetField(t, facets, "chef.node.identity", "platform", "ubuntu")
	assertFacetField(t, facets, "chef.node.identity", "chef_client", "15.17.4")
	assertFacetField(t, facets, "chef.node.os", "kernel_release", "5.15.0-91-generic")
	assertFacetField(t, facets, "chef.node.network", "ipv4", "10.0.0.10")

	// The View / smart-inventory story: select production hosts by label.
	prod, err := store.ResolveSelector(ctx, types.ViewSelector{Kinds: []string{"host"}, Labels: map[string]string{"chef.environment": "production"}}, nil, 0)
	if err != nil || len(prod) != 2 {
		t.Fatalf("production View: got %d err=%v, want 2 (web-01, db-01)", len(prod), err)
	}

	// Removal → tombstone on the next full enumeration.
	sim.Remove("cache-01")
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := store.CountSelector(ctx, types.ViewSelector{Kinds: []string{"host"}}); err != nil || n != 2 {
		t.Fatalf("after removal: got %d err=%v, want 2", n, err)
	}
}

func findHost(t *testing.T, store *graph.Store, nodeName string) types.Entity {
	t.Helper()
	ents, err := store.ResolveSelector(context.Background(), types.ViewSelector{Kinds: []string{"host"}}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.IdentityKeys["chef.node.name"] == nodeName {
			return e
		}
	}
	t.Fatalf("host %s not found", nodeName)
	return types.Entity{}
}

func facetMap(t *testing.T, store *graph.Store, entityID string) map[string]map[string]any {
	t.Helper()
	facets, err := store.GetFacets(context.Background(), entityID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, f := range facets {
		doc := map[string]any{}
		if err := json.Unmarshal(f.Value, &doc); err != nil {
			t.Fatalf("unmarshal facet %s: %v", f.Namespace, err)
		}
		out[f.Namespace] = doc
	}
	return out
}

func assertFacetField(t *testing.T, facets map[string]map[string]any, ns, field, want string) {
	t.Helper()
	doc, ok := facets[ns]
	if !ok {
		t.Fatalf("facet %s not projected (have %v)", ns, keysOf(facets))
	}
	if got, _ := doc[field].(string); got != want {
		t.Fatalf("facet %s.%s: got %q want %q", ns, field, got, want)
	}
}

func keysOf(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
