package orchestrate

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/pluginhost"
)

// A governed Apply write-back's Facets survive the gRPC door.
//
// THEY DID NOT, AND THE GOVERNOR WAS DOING THE WORK ANYWAY. `pluginhost.ApplyEntity` has always
// carried `Facets`, and the Apply governor gates each namespace against the operator grant ∩ the
// Step's facetWriteScope (host.go:1101), emitting a Rejection for every one it refuses. The gRPC
// door then built an EntityObservation out of Kind/IdentityKeys/Labels and dropped the survivors —
// while the EE-Job door, for the same governed shape, routed them to res.Facts and projected them.
// One request shape, two transports, two fates.
//
// Nothing shipping hit it, and the way it did not hit it is the reason to fix it rather than to
// leave it: `plugins/dns/estate/actuators/dns.yaml` and `plugins/awsec2/estate/actuators/awsec2.yaml`
// each DECLINE the facetNamespaces grant, both explaining in a comment that it would be "authority
// granted for a path that does not exist". The estate had learned to route around a hole in core.
func TestGrpcApplyWriteBackCarriesItsGovernedFacets(t *testing.T) {
	wb := []pluginhost.ApplyEntity{{
		Kind:         "host",
		IdentityKeys: map[string]string{"host.name": "web-02"},
		Labels:       map[string]string{"fleet": "web"},
		Facets: map[string][]byte{
			"app.config":   []byte(`{"port":"8080"}`),
			"mgmt.address": []byte(`{"address":"web-02.svc"}`),
		},
	}}
	obs := writeBackObservations(wb)
	if len(obs) != 1 {
		t.Fatalf("expected one observation, got %d", len(obs))
	}
	if got := len(obs[0].Facets); got != 2 {
		t.Fatalf("the governed write-back carried 2 facets; the observation kept %d. A facet the "+
			"governor ADMITTED and nothing writes reports as nothing at all (ADR-0054)", got)
	}
	var cfg struct {
		Port string `json:"port"`
	}
	if err := json.Unmarshal(obs[0].Facets["app.config"], &cfg); err != nil {
		t.Fatalf("app.config did not survive as valid JSON: %v", err)
	}
	if cfg.Port != "8080" {
		t.Errorf("app.config.port = %q, want 8080 — the value must cross unmodified", cfg.Port)
	}
	// Identity and labels are unchanged by the fix; asserted so a regression here is not
	// mistaken for the facet change.
	if obs[0].IdentityKeys["host.name"] != "web-02" || obs[0].Labels["fleet"] != "web" {
		t.Errorf("identity/labels must cross unchanged, got %v / %v", obs[0].IdentityKeys, obs[0].Labels)
	}
}

// Both Apply doors lift Facets through the SAME function, so they cannot disagree again.
//
// The EE-Job door keys its facts by the write-back's host.name (the confused-deputy floor:
// CollectFacts drops any name outside the core-resolved target set) and the gRPC door keys its
// facets to the Entity it upserts by identity. Those routes are legitimately different. What must
// NOT differ is what gets carried, which is why the lift is one function rather than two loops —
// the duplicated loop is precisely what let one door keep the facets and the other forget them.
func TestBothApplyDoorsLiftFacetsThroughOneFunction(t *testing.T) {
	e := pluginhost.ApplyEntity{
		IdentityKeys: map[string]string{"host.name": "web-02"},
		Facets:       map[string][]byte{"app.config": []byte(`{"port":"80"}`)},
	}
	viaJobDoor := applyFacets(e) // what executeJobPlugin puts in res.Facts[name]
	viaGrpcDoor := writeBackObservations([]pluginhost.ApplyEntity{e})[0].Facets
	if !reflect.DeepEqual(viaJobDoor, viaGrpcDoor) {
		t.Errorf("the two Apply transports disagree about what a write-back carries:\n  EE-Job: %v\n  gRPC:   %v",
			viaJobDoor, viaGrpcDoor)
	}
}

// Every field of ApplyEntity is consumed by the gRPC door.
//
// A STRUCTURAL FLOOR, not a restatement of the tests above. The original defect was not a wrong
// mapping — it was a field that existed on the governed shape and had no consumer, which no
// value-comparing test can notice because there is no value to compare against. This one fails when
// someone ADDS a field to ApplyEntity and updates only the door they were working in, which is
// exactly how the first divergence happened.
//
// If a future field genuinely belongs to only one transport, add it to `deliberatelyUnconsumed`
// with the reason — "we looked and said no" must not render the same as "nobody looked".
func TestEveryApplyEntityFieldReachesTheObservation(t *testing.T) {
	deliberatelyUnconsumed := map[string]string{}

	obsFields := map[string]bool{}
	ot := reflect.TypeOf(writeBackObservations(nil))
	for i := 0; i < ot.Elem().NumField(); i++ {
		obsFields[ot.Elem().Field(i).Name] = true
	}
	at := reflect.TypeOf(pluginhost.ApplyEntity{})
	for i := 0; i < at.NumField(); i++ {
		name := at.Field(i).Name
		if why, ok := deliberatelyUnconsumed[name]; ok {
			t.Logf("ApplyEntity.%s is deliberately not carried: %s", name, why)
			continue
		}
		if !obsFields[name] {
			t.Errorf("pluginhost.ApplyEntity.%s has no counterpart on actuators.EntityObservation, so "+
				"the gRPC Apply door governs it and then discards it. Either carry it (and project it "+
				"in ProjectFacts) or record it in deliberatelyUnconsumed with the reason", name)
		}
	}
}
