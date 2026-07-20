package netbox

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeNetBox serves a minimal IPAM: two VLANs and two prefixes (one bound to a
// VLAN, one not), so enumerate can be tested with no live NetBox and no core.
func fakeNetBox(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ipam/vlans/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token secret" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		io.WriteString(w, `{"next":null,"results":[
			{"id":10,"vid":100,"name":"web"},
			{"id":11,"vid":200,"name":"db"}]}`)
	})
	mux.HandleFunc("/api/ipam/prefixes/", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"next":null,"results":[
			{"id":1,"prefix":"10.0.1.0/24","status":{"value":"active"},"vlan":{"id":10,"vid":100,"name":"web"}},
			{"id":2,"prefix":"10.0.2.0/24","status":{"value":"active"},"vlan":null}]}`)
	})
	return httptest.NewServer(mux)
}

// TestEnumerateProjectsTopology proves the content-expertise: NetBox VLANs → `vlan`
// Entities, prefixes → `subnet` Entities, and a prefix bound to a VLAN carries the
// `in-vlan` placement Relation targeting that VLAN by identity (ADR-0059).
func TestEnumerateProjectsTopology(t *testing.T) {
	ts := fakeNetBox(t)
	defer ts.Close()
	s := NewServer(Config{Endpoint: ts.URL, Token: "secret"}, discard())

	ents, err := s.enumerate(context.Background())
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	var subnets, vlans int
	var web *pluginv1.ObservedEntity
	for _, e := range ents {
		switch e.GetKind() {
		case "vlan":
			vlans++
			if e.GetIdentityKeys()["netbox.vlan.id"] == "" {
				t.Errorf("vlan missing netbox.vlan.id identity")
			}
			if len(e.GetFacets()["net.vlan"]) == 0 {
				t.Errorf("vlan missing net.vlan facet")
			}
		case "subnet":
			subnets++
			if e.GetIdentityKeys()["netbox.prefix.id"] == "" {
				t.Errorf("subnet missing netbox.prefix.id identity")
			}
			if e.GetLabels()["net.cidr"] == "10.0.1.0/24" {
				web = e
			}
		default:
			t.Errorf("unexpected kind %q", e.GetKind())
		}
	}
	if subnets != 2 || vlans != 2 {
		t.Fatalf("expected 2 subnets + 2 vlans, got %d/%d", subnets, vlans)
	}
	// The VLAN-bound prefix carries the in-vlan placement edge, targeting the VLAN
	// by identity; the un-bound one does not.
	if web == nil {
		t.Fatal("did not find the 10.0.1.0/24 subnet")
	}
	if len(web.GetRelations()) != 1 {
		t.Fatalf("web subnet should carry exactly one in-vlan relation, got %d", len(web.GetRelations()))
	}
	rel := web.GetRelations()[0]
	if rel.GetType() != "in-vlan" || rel.GetToScheme() != "netbox.vlan.id" || rel.GetToValue() != "10" {
		t.Errorf("in-vlan relation = %+v, want →netbox.vlan.id=10", rel)
	}
}

// TestManifest locks the syncer surface: SYNCER/OBSERVE, the two owned-but-uncovered
// Facet namespaces, and the netbox-native tombstone schemes.
func TestManifest(t *testing.T) {
	m, err := NewServer(Config{}, discard()).GetManifest(context.Background(), &pluginv1.GetManifestRequest{})
	if err != nil {
		t.Fatal(err)
	}
	man := m.GetManifest()
	if man.GetClass() != pluginv1.PluginClass_PLUGIN_CLASS_SYNCER {
		t.Errorf("class = %v, want SYNCER", man.GetClass())
	}
	ns := map[string]bool{}
	for _, c := range man.GetContracts() {
		ns[c.GetSchemaId()] = true
	}
	if !ns["net.subnet"] || !ns["net.vlan"] {
		t.Errorf("manifest must request net.subnet + net.vlan ownership, got %v", ns)
	}
	if len(man.GetTombstoneSchemes()) != 2 {
		t.Errorf("expected netbox.prefix.id + netbox.vlan.id tombstone schemes, got %v", man.GetTombstoneSchemes())
	}
}
