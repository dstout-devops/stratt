package chef

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/dstout-devops/stratt/plugins/chef/chefsim"
)

func linuxNode(name, env, fqdn string) chefsim.Node {
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
	return chefsim.Node{Name: name, Environment: env, Automatic: auto}
}

// TestEnumerateAgainstSim proves the plugin's content-expertise in isolation —
// the signature-verifying Chef sim in-process, no core, no Postgres. It asserts
// the go-chef→ObservedEntity mapping the wire carries: kind, identity keys
// (including the cross-source dns.fqdn the plugin proposes), and this plugin's
// Facet blobs. (The host side of the wire is proven separately in core, so
// neither module imports the other — the module-isolation point of Phase C.)
func TestEnumerateAgainstSim(t *testing.T) {
	key, keyPEM, err := chefsim.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sim := chefsim.New("acme", "stratt", key)
	sim.Set(linuxNode("web-01", "production", "web-01.acme.internal"))
	sim.Set(linuxNode("db-01", "production", "")) // no fqdn → name-only identity
	srv := httptest.NewServer(sim.Handler())
	defer srv.Close()

	cfg := Config{
		ServerURL:  srv.URL + "/organizations/acme/",
		ClientName: "stratt",
		KeyPEM:     keyPEM,
	}
	client, err := connect(cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	entities, err := enumerate(context.Background(), client)
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	if len(entities) != 2 {
		t.Fatalf("expected 2 host entities, got %d", len(entities))
	}

	byName := map[string]bool{}
	var withFQDN int
	for _, e := range entities {
		if e.GetKind() != "host" {
			t.Errorf("unexpected kind %q", e.GetKind())
		}
		name := e.GetIdentityKeys()["chef.node.name"]
		if name == "" {
			t.Errorf("host missing chef.node.name identity: %v", e.GetIdentityKeys())
		}
		byName[name] = true

		// Facet blobs the plugin curates from ohai must be present + valid JSON.
		if blob := e.GetFacets()["chef.node.identity"]; len(blob) == 0 {
			t.Errorf("%s missing chef.node.identity facet blob", name)
		} else {
			var doc map[string]any
			if err := json.Unmarshal(blob, &doc); err != nil {
				t.Errorf("%s chef.node.identity not valid json: %v", name, err)
			}
			if doc["platform"] != "ubuntu" {
				t.Errorf("%s chef.node.identity.platform = %v, want ubuntu", name, doc["platform"])
			}
		}
		if len(e.GetFacets()["chef.node.os"]) == 0 {
			t.Errorf("%s missing chef.node.os facet blob", name)
		}
		if len(e.GetFacets()["chef.node.network"]) == 0 {
			t.Errorf("%s missing chef.node.network facet blob", name)
		}

		// The plugin proposes the shared cross-source dns.fqdn identity when ohai
		// reports it — the host gates whether it may be written (ADR-0046 #4).
		if e.GetIdentityKeys()["dns.fqdn"] != "" {
			withFQDN++
		}
	}

	if !byName["web-01"] || !byName["db-01"] {
		t.Fatalf("expected web-01 and db-01, got %v", byName)
	}
	if withFQDN != 1 {
		t.Fatalf("expected exactly one entity carrying dns.fqdn (web-01), got %d", withFQDN)
	}
}
