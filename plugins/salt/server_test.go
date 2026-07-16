package salt

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/dstout-devops/stratt/plugins/salt/saltsim"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func slogDiscard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
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

// TestEnumerateAgainstSim proves the plugin's content-expertise in isolation —
// saltsim over httptest, no core, no Postgres. It drives the real code path
// (eauth login → runner cache.grains → normalize) and asserts the salt-api→
// ObservedEntity mapping the wire carries: kind, identity, and this plugin's
// Facet blobs. (The host side of the wire is proven separately in core, so
// neither module imports the other — the module-isolation point of Phase C.)
func TestEnumerateAgainstSim(t *testing.T) {
	sim := saltsim.New()
	sim.SetMinion("web-01.acme.internal", minionGrains("web-01.acme.internal", "web-01.acme.internal", "RedHat"))
	sim.SetMinion("db-01.acme.internal", minionGrains("db-01.acme.internal", "db-01.acme.internal", "RedHat"))
	sim.SetMinion("edge-01.acme.internal", minionGrains("edge-01.acme.internal", "", "Debian")) // no fqdn
	srv := httptest.NewServer(sim.Handler())
	defer srv.Close()

	ctx := context.Background()
	client, err := connect(ctx, Config{APIURL: srv.URL, Username: "stratt", Password: "pw"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	entities, err := enumerate(ctx, client)
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	if len(entities) != 3 {
		t.Fatalf("expected 3 host entities, got %d", len(entities))
	}

	byMinion := map[string]bool{}
	fqdnCarriers := 0
	for _, e := range entities {
		if e.GetKind() != "host" {
			t.Errorf("unexpected kind %q, want host", e.GetKind())
		}
		id := e.GetIdentityKeys()["salt.minion_id"]
		if id == "" {
			t.Errorf("host missing salt.minion_id identity: %v", e.GetIdentityKeys())
			continue
		}
		byMinion[id] = true
		if len(e.GetFacets()["salt.node.identity"]) == 0 {
			t.Errorf("%s missing salt.node.identity facet blob", id)
		}
		if len(e.GetFacets()["salt.node.os"]) == 0 {
			t.Errorf("%s missing salt.node.os facet blob", id)
		}
		if len(e.GetFacets()["salt.node.network"]) == 0 {
			t.Errorf("%s missing salt.node.network facet blob", id)
		}
		if e.GetIdentityKeys()["dns.fqdn"] != "" {
			fqdnCarriers++
		}
	}

	// web-01/db-01 carry the shared dns.fqdn identity; edge-01 (no fqdn grain)
	// must NOT emit an empty dns.fqdn key.
	if fqdnCarriers != 2 {
		t.Errorf("expected 2 hosts carrying dns.fqdn, got %d", fqdnCarriers)
	}
	edge := findEntity(t, entities, "edge-01.acme.internal")
	if _, ok := edge.GetIdentityKeys()["dns.fqdn"]; ok {
		t.Errorf("edge-01 has no fqdn grain; must not emit dns.fqdn, got %v", edge.GetIdentityKeys())
	}

	for _, want := range []string{"web-01.acme.internal", "db-01.acme.internal", "edge-01.acme.internal"} {
		if !byMinion[want] {
			t.Errorf("missing minion %s from enumeration", want)
		}
	}
}

// TestGetManifestSyncer asserts the advertised manifest: SYNCER class, OBSERVE
// verb, the salt.node.* facet Contracts, and the salt.minion_id tombstone scheme
// (the plugin's OWN identity scheme — never the shared dns.fqdn).
func TestGetManifestSyncer(t *testing.T) {
	s := NewServer(Config{}, slogDiscard())
	resp, err := s.GetManifest(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	m := resp.GetManifest()
	if m.GetClass().String() != "PLUGIN_CLASS_SYNCER" {
		t.Errorf("class = %s, want PLUGIN_CLASS_SYNCER", m.GetClass())
	}
	if len(m.GetVerbs()) != 1 || m.GetVerbs()[0].String() != "VERB_OBSERVE" {
		t.Errorf("verbs = %v, want [VERB_OBSERVE]", m.GetVerbs())
	}
	gotContracts := map[string]bool{}
	for _, c := range m.GetContracts() {
		gotContracts[c.GetSchemaId()] = true
	}
	for _, ns := range []string{"salt.node.identity", "salt.node.os", "salt.node.network"} {
		if !gotContracts[ns] {
			t.Errorf("manifest missing contract %s", ns)
		}
	}
	if len(m.GetTombstoneSchemes()) != 1 || m.GetTombstoneSchemes()[0] != "salt.minion_id" {
		t.Errorf("tombstone schemes = %v, want [salt.minion_id]", m.GetTombstoneSchemes())
	}
}

func findEntity(t *testing.T, entities []*pluginv1.ObservedEntity, minionID string) *pluginv1.ObservedEntity {
	t.Helper()
	for _, e := range entities {
		if e.GetIdentityKeys()["salt.minion_id"] == minionID {
			return e
		}
	}
	t.Fatalf("entity %s not found", minionID)
	return nil
}
