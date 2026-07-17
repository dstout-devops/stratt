package puppet

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/dstout-devops/stratt/plugins/puppet/puppetsim"
)

func facterNode(certname, env, fqdn string) puppetsim.Node {
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
	return puppetsim.Node{
		Certname: certname, Environment: env, Facts: facts,
		Trusted: map[string]any{"certname": certname},
	}
}

// newTestServer wires a plugin Server at a puppetsim-backed BaseURL with a small
// page size so the paged enumeration is exercised across the seeded nodes.
func newTestServer(t *testing.T, sim *puppetsim.Sim) (*Server, func()) {
	t.Helper()
	srv := httptest.NewServer(sim.Handler())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := NewServer(Config{BaseURL: srv.URL, PluginID: "puppet"}, log)
	s.pageLimit = 2 // exercise pagination across the seeded nodes
	client, err := connect(s.cfg)
	if err != nil {
		srv.Close()
		t.Fatalf("connect: %v", err)
	}
	s.client = client
	return s, srv.Close
}

// TestEnumerateAgainstSim proves the plugin's content-expertise in isolation —
// puppetsim in-process over httptest, no core, no Postgres. It asserts the
// PuppetDB /inventory → ObservedEntity mapping the wire carries: kind, identity,
// and this plugin's Facet blobs. (The host side of the wire is proven separately
// in core, so neither module imports the other — the module-isolation point.)
func TestEnumerateAgainstSim(t *testing.T) {
	sim := puppetsim.New()
	sim.Set(facterNode("web-01.acme.internal", "production", "web-01.acme.internal"))
	sim.Set(facterNode("db-01.acme.internal", "production", "")) // no fqdn → certname-only identity
	sim.Set(facterNode("cache-01.acme.internal", "staging", "cache-01.acme.internal"))

	s, closeSim := newTestServer(t, sim)
	defer closeSim()

	entities, err := s.enumerate(context.Background())
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	if len(entities) != 3 {
		t.Fatalf("expected 3 host entities, got %d", len(entities))
	}

	byCertname := map[string]bool{}
	fqdnCarrying := 0
	for _, e := range entities {
		if e.GetKind() != "host" {
			t.Errorf("unexpected kind %q, want host", e.GetKind())
		}
		certname := e.GetIdentityKeys()["puppet.certname"]
		if certname == "" {
			t.Errorf("host missing puppet.certname identity")
			continue
		}
		byCertname[certname] = true
		if e.GetIdentityKeys()["dns.fqdn"] != "" {
			fqdnCarrying++
		}
		// Facet blobs: identity + os always populated for the seeded nodes.
		if len(e.GetFacets()["puppet.node.identity"]) == 0 {
			t.Errorf("%s missing puppet.node.identity facet blob", certname)
		}
		if len(e.GetFacets()["puppet.node.os"]) == 0 {
			t.Errorf("%s missing puppet.node.os facet blob", certname)
		}
		if len(e.GetFacets()["puppet.node.network"]) == 0 {
			t.Errorf("%s missing puppet.node.network facet blob", certname)
		}
	}
	if !byCertname["web-01.acme.internal"] || !byCertname["db-01.acme.internal"] || !byCertname["cache-01.acme.internal"] {
		t.Fatalf("missing expected certnames, got %v", byCertname)
	}
	// web-01 and cache-01 carry dns.fqdn; db-01 (no fqdn) is certname-only.
	if fqdnCarrying != 2 {
		t.Fatalf("expected 2 hosts carrying dns.fqdn identity, got %d", fqdnCarrying)
	}
	t.Logf("enumerated %d hosts (%d carrying dns.fqdn)", len(entities), fqdnCarrying)
}

// TestFacetBlobContents proves the emitted Facet blobs carry the curated
// charter-down values (never a Facter dump) — os_family/environment on identity,
// kernelrelease on os, ipv4/fqdn on network.
func TestFacetBlobContents(t *testing.T) {
	sim := puppetsim.New()
	sim.Set(facterNode("web-01.acme.internal", "production", "web-01.acme.internal"))

	s, closeSim := newTestServer(t, sim)
	defer closeSim()

	entities, err := s.enumerate(context.Background())
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	if len(entities) != 1 {
		t.Fatalf("expected 1 entity, got %d", len(entities))
	}
	e := entities[0]
	if e.GetIdentityKeys()["dns.fqdn"] != "web-01.acme.internal" {
		t.Fatalf("web-01 must carry the fqdn identity, got %v", e.GetIdentityKeys())
	}
	assertFacetField(t, e.GetFacets(), "puppet.node.identity", "os_family", "RedHat")
	assertFacetField(t, e.GetFacets(), "puppet.node.identity", "environment", "production")
	assertFacetField(t, e.GetFacets(), "puppet.node.os", "kernelrelease", "5.14.0-427.el9")
	assertFacetField(t, e.GetFacets(), "puppet.node.network", "ipv4", "10.1.0.20")
}

func assertFacetField(t *testing.T, facets map[string][]byte, ns, field, want string) {
	t.Helper()
	raw, ok := facets[ns]
	if !ok {
		t.Fatalf("facet %s not emitted", ns)
	}
	doc := map[string]any{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal facet %s: %v", ns, err)
	}
	if got, _ := doc[field].(string); got != want {
		t.Fatalf("facet %s.%s: got %q want %q", ns, field, got, want)
	}
}
