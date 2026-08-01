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
	// The plugin must say WHICH of its Actions IS the `ipam` class (ADR-0140 D1). Core used to
	// compute the answer from the plugin id; it now reads this, and a provider that stops
	// advertising it stops resolving — so the assertion belongs here, not only in core.
	var impl string
	for _, a := range man.GetActions() {
		if a.GetImplements() == "ipam" {
			impl = a.GetName()
		}
	}
	if impl != actionIPAMResolve {
		t.Errorf("the ipam implementation must be advertised as %q, got %q — declaring provides:[ipam] "+
			"without naming the Action that IS it leaves the class unresolvable", actionIPAMResolve, impl)
	}
}

// TestAuthorizationHeaderCoversBothTokenGenerations pins the scheme selection against NetBox's own
// rule, because getting it wrong is undiagnosable from the error the server returns.
//
// NetBox 4.5 replaced the API token: v1 is a 40-char plaintext looked up directly, v2 is
// `nbt_<12-char key>.<plaintext>` looked up by key and HMAC-validated against a server-side pepper.
// The server infers the version from the prefix and nothing else
// (netbox/api/authentication.py: `version = 2 if auth_value.startswith(TOKEN_PREFIX) else 1`), so
// this function does too — one discriminator, on both sides.
//
// The failure mode this prevents: presenting a v2 token under `Token ` parses as v1, finds no v1
// row, and returns `{"detail":"Invalid v1 token"}` — a message naming the version you did NOT
// intend, while the token you supplied is perfectly valid. That cost a long diagnosis, including a
// wrong turn pinning the dev floor down a major version to avoid it.
func TestAuthorizationHeaderCoversBothTokenGenerations(t *testing.T) {
	for _, tc := range []struct {
		name, token, want string
	}{
		{"v1 plaintext", "0123456789abcdef0123456789abcdef01234567", "Token 0123456789abcdef0123456789abcdef01234567"},
		{"v2 prefixed", "nbt_QIh94I6HmYNK.8fRnOCKYauRPZapw0bwqr1p8abnc5uuks0jfedquvs7", "Bearer nbt_QIh94I6HmYNK.8fRnOCKYauRPZapw0bwqr1p8abnc5uuks0jfedquvs7"},
		// A v2-looking value with no dot is still sent as Bearer: the PREFIX is the discriminator on
		// the server, so second-guessing it here would make the plugin and NetBox disagree about
		// what a token is — and the plugin would lose.
		{"prefix without a key separator", "nbt_malformed", "Bearer nbt_malformed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := authorizationHeader(tc.token); got != tc.want {
				t.Fatalf("authorizationHeader(%q) = %q, want %q", tc.token, got, tc.want)
			}
		})
	}
}
