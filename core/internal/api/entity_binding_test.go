package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// bindEntityParams is stage two of the per-Entity binding (ADR-0150 D2): the compiler defers
// `{{.entity.*}}` because a Baseline covers a whole View, and the Finding — which names exactly one
// Entity — is where it resolves. Injected store read, per this package's rule that the DECISION
// half runs in `task ci` rather than behind a Postgres skip.
func webHost(string) (map[string]any, error) {
	return map[string]any{
		"id":   "e-1",
		"dns":  map[string]any{"fqdn": "web-1.stratt.test"},
		"mgmt": map[string]any{"address": map[string]any{"address": "10.30.0.11"}},
	}, nil
}

func TestBindEntityParams_DerivesPerHostValue(t *testing.T) {
	fl := findingLaunch{Baseline: "b1", Params: map[string]any{
		"commonName":  "{{.entity.dns.fqdn}}",
		"renewBefore": "360h", // already resolved at compile from {{.spec.renewBefore}}
	}}
	if prob := bindEntityParams(&fl, types.Finding{ID: "f1", EntityID: "e-1", Target: "web-1"}, webHost); prob != nil {
		t.Fatalf("binding must succeed: %+v", prob)
	}
	if fl.Params["commonName"] != "web-1.stratt.test" {
		t.Fatalf("commonName must be DERIVED from the host, got %q", fl.Params["commonName"])
	}
	if fl.Params["renewBefore"] != "360h" {
		t.Fatalf("a compile-resolved param must be untouched, got %q", fl.Params["renewBefore"])
	}
}

// The whole point of D2. A host missing the Facet the naming policy asks for gets NO certificate
// and a refusal that names the Entity — never a fallback, because the value a fallback picks is a
// certificate issued for the wrong subject.
func TestBindEntityParams_FailsClosedOnAMissingFacet(t *testing.T) {
	noFQDN := func(string) (map[string]any, error) {
		return map[string]any{"id": "e-2", "mgmt": map[string]any{"address": map[string]any{"address": "10.30.0.12"}}}, nil
	}
	fl := findingLaunch{Baseline: "b1", Params: map[string]any{"commonName": "{{.entity.dns.fqdn}}"}}
	prob := bindEntityParams(&fl, types.Finding{ID: "f2", EntityID: "e-2", Target: "web-2"}, noFQDN)
	if prob == nil {
		t.Fatal("a host missing the named Facet must be REFUSED, not given a fallback subject")
	}
	if prob.Status != http.StatusConflict {
		t.Fatalf("status: %d", prob.Status)
	}
	for _, want := range []string{"f2", "e-2", "web-2", "b1", "dns.fqdn"} {
		if !strings.Contains(prob.Message, want) {
			t.Fatalf("the refusal must name %q so the operator can act on it (§1.8): %s", want, prob.Message)
		}
	}
	if v := fl.Params["commonName"]; v != "{{.entity.dns.fqdn}}" {
		t.Fatalf("a refused binding must not partially mutate the launch, got %q", v)
	}
}

// A per-Entity binding on a Finding whose target is not an Entity has nothing to resolve against.
func TestBindEntityParams_RefusesANonEntityTarget(t *testing.T) {
	fl := findingLaunch{Baseline: "b1", Params: map[string]any{"commonName": "{{.entity.dns.fqdn}}"}}
	prob := bindEntityParams(&fl, types.Finding{ID: "f3", Target: "workspace/app-subnet"}, webHost)
	if prob == nil || prob.Status != http.StatusConflict {
		t.Fatalf("a non-Entity target must be refused, got %+v", prob)
	}
	if !strings.Contains(prob.Message, "workspace/app-subnet") {
		t.Fatalf("the refusal must name the target: %s", prob.Message)
	}
}

// Every Baseline that uses no per-Entity token must be untouched — including one whose Finding has
// no Entity at all, so this costs nothing for the estate that shipped before ADR-0150.
func TestBindEntityParams_LeavesOrdinaryLaunchesAlone(t *testing.T) {
	fl := findingLaunch{Params: map[string]any{"apache_port": "8080"}}
	boom := func(string) (map[string]any, error) { return nil, fmt.Errorf("must not be read") }
	if prob := bindEntityParams(&fl, types.Finding{ID: "f4"}, boom); prob != nil {
		t.Fatalf("a launch with no entity token must not bind or read: %+v", prob)
	}
	if fl.Params["apache_port"] != "8080" {
		t.Fatalf("params must be untouched, got %q", fl.Params["apache_port"])
	}
}
