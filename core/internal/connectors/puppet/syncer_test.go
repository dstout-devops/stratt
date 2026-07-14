package puppet

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

	"github.com/dstout-devops/stratt/core/internal/connectors/puppet/puppetdbsim"
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

func facterNode(certname, env, fqdn string) puppetdbsim.Node {
	facts := map[string]any{
		"os": map[string]any{
			"name":         "CentOS",
			"family":       "RedHat",
			"architecture": "x86_64",
			"release":      map[string]any{"full": "9.4"},
		},
		"kernel":        "Linux",
		"kernelrelease": "5.14.0-427.el9",
		"kernelversion": "5.14.0",
		"networking": map[string]any{
			"ip":  "10.1.0.20",
			"ip6": "fe80::1",
			"mac": "aa:bb:cc:dd:ee:ff",
		},
	}
	if fqdn != "" {
		facts["networking"].(map[string]any)["fqdn"] = fqdn
	}
	return puppetdbsim.Node{
		Certname: certname, Environment: env, Facts: facts,
		Trusted: map[string]any{"certname": certname},
	}
}

// TestPuppetSyncerProjectsAndUnifies drives the real Syncer against puppetdbsim:
// paged full enumeration projects host Entities with puppet.node.* facets and
// labels; a removed node is tombstoned; and — the generality proof — a host
// already observed by Chef (same dns.fqdn) UNIFIES into one Entity carrying both
// puppet.node.* and chef.node.* facets, each with its own Source provenance.
func TestPuppetSyncerProjectsAndUnifies(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	sim := puppetdbsim.New()
	sim.Set(facterNode("web-01.acme.internal", "production", "web-01.acme.internal"))
	sim.Set(facterNode("db-01.acme.internal", "production", "")) // no fqdn → certname-only identity
	sim.Set(facterNode("cache-01.acme.internal", "staging", "cache-01.acme.internal"))
	srv := httptest.NewServer(sim.Handler())
	defer srv.Close()

	cfg := Config{BaseURL: srv.URL, SourceName: "pdb"}
	s := NewSyncer(cfg, time.Minute, store, log)
	s.pageLimit = 2 // exercise pagination across the 3 nodes
	if err := s.Register(ctx); err != nil {
		t.Fatal(err)
	}
	client, err := cfg.httpClient()
	if err != nil {
		t.Fatal(err)
	}
	s.client = client

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
	// Selectable, source-attributable data lives in the source-scoped facets,
	// NOT the shared Entity label bag (which would clobber across Sources, §2.4).
	facets := facetMap(t, store, web.ID)
	assertFacetField(t, facets, "puppet.node.identity", "os_family", "RedHat")
	assertFacetField(t, facets, "puppet.node.identity", "environment", "production")
	assertFacetField(t, facets, "puppet.node.os", "kernelrelease", "5.14.0-427.el9")
	assertFacetField(t, facets, "puppet.node.network", "ipv4", "10.1.0.20")

	// The smart-inventory story: select production hosts via the source-scoped
	// facet (the shipped example View's selector).
	prod, err := store.ResolveSelector(ctx, types.ViewSelector{
		Kinds:  []string{"host"},
		Facets: []types.FacetPredicate{{Namespace: "puppet.node.identity", Path: "environment", Equals: json.RawMessage(`"production"`)}},
	}, nil, 0)
	if err != nil || len(prod) != 2 {
		t.Fatalf("production View: got %d err=%v, want 2", len(prod), err)
	}

	// ── Cross-source unification (the generality proof) ──────────────────
	// Chef observed the same host by fqdn: register its owner and project a
	// chef.node.identity facet onto the shared dns.fqdn. It must land on the
	// SAME Entity, coexisting with the Puppet facets — no duplicate, no clobber.
	chefSrc, err := store.RegisterSource(ctx, types.Source{Kind: "chef", Name: "acme-chef"})
	if err != nil {
		t.Fatal(err)
	}
	chefRef := "connector/chef/acme-chef/syncer"
	if err := store.RegisterFacetOwner(ctx, types.FacetOwner{Namespace: "chef.node.identity", OwnerKind: "syncer", OwnerRef: chefRef}); err != nil {
		t.Fatal(err)
	}
	chefProv := types.Provenance{WriterKind: types.WriterSyncer, WriterRef: chefRef, SourceID: chefSrc.ID, At: time.Now().UTC()}
	if _, err := store.NormalizerProjector().UpsertEntities(ctx, chefProv, []graph.EntityUpsert{{
		Kind:         "host",
		IdentityKeys: map[string]string{"dns.fqdn": "web-01.acme.internal"},
		Facets:       map[string]json.RawMessage{"chef.node.identity": json.RawMessage(`{"platform":"centos"}`)},
	}}); err != nil {
		t.Fatal(err)
	}
	if n, err := store.CountSelector(ctx, types.ViewSelector{Kinds: []string{"host"}}); err != nil || n != 3 {
		t.Fatalf("after Chef observation: got %d err=%v, want 3 (unify, not duplicate)", n, err)
	}
	merged := facetMap(t, store, web.ID)
	if _, ok := merged["puppet.node.identity"]; !ok {
		t.Fatal("Puppet facet clobbered by the Chef observation")
	}
	if _, ok := merged["chef.node.identity"]; !ok {
		t.Fatal("Chef facet did not land on the unified Entity")
	}
	// Each source-scoped facet keeps its own Source provenance (§2.1).
	if provSource(t, store, web.ID, "puppet.node.identity") == provSource(t, store, web.ID, "chef.node.identity") {
		t.Fatal("the two source-scoped facets must carry distinct Source provenance")
	}

	// Removal → tombstone on the next enumeration.
	sim.Remove("cache-01.acme.internal")
	if err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := store.CountSelector(ctx, types.ViewSelector{Kinds: []string{"host"}}); err != nil || n != 2 {
		t.Fatalf("after removal: got %d err=%v, want 2", n, err)
	}
}

func findHost(t *testing.T, store *graph.Store, certname string) types.Entity {
	t.Helper()
	ents, err := store.ResolveSelector(context.Background(), types.ViewSelector{Kinds: []string{"host"}}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.IdentityKeys["puppet.certname"] == certname {
			return e
		}
	}
	t.Fatalf("host %s not found", certname)
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

func provSource(t *testing.T, store *graph.Store, entityID, ns string) string {
	t.Helper()
	return facetMap(t, store, entityID)[ns].Provenance.SourceID
}
