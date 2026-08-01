package template

import "testing"

// Deferral is a TWO-STAGE binding, not a hole in the fail-closed rule (ADR-0024 D1, ADR-0150 D2).
// A deferred namespace passes through with its token text intact so a later stage can resolve it;
// a namespace nobody deferred is still an error, so a typo fails where it always did.
func TestSubstituteDeferring(t *testing.T) {
	ns := Namespaces{"spec": {"renewBefore": "360h"}}
	deferred := map[string]bool{"entity": true}

	got, err := SubstituteParamsDeferring(map[string]any{
		"commonName":  "{{.entity.dns.fqdn}}",             // exact token, deferred
		"label":       "cert for {{.entity.name}} (prod)", // embedded token, deferred
		"renewBefore": "{{.spec.renewBefore}}",            // resolved now
		"literal":     "api.example.com",                  // untouched
	}, ns, deferred)
	if err != nil {
		t.Fatalf("deferred namespace must not error: %v", err)
	}
	if got["commonName"] != "{{.entity.dns.fqdn}}" {
		t.Fatalf("an exact deferred token must survive VERBATIM for the next stage, got %q", got["commonName"])
	}
	if got["label"] != "cert for {{.entity.name}} (prod)" {
		t.Fatalf("an embedded deferred token must survive in place, got %q", got["label"])
	}
	if got["renewBefore"] != "360h" {
		t.Fatalf("a non-deferred namespace must still resolve, got %q", got["renewBefore"])
	}
	if got["literal"] != "api.example.com" {
		t.Fatalf("a literal must pass through, got %q", got["literal"])
	}

	// The fail-closed rule is UNCHANGED for anything not explicitly deferred — including a typo of
	// the deferred namespace itself, which is the mistake deferral could most easily have hidden.
	if _, err := SubstituteParamsDeferring(map[string]any{"x": "{{.entty.dns.fqdn}}"}, ns, deferred); err == nil {
		t.Fatal("a namespace nobody deferred must still fail closed")
	}
	if _, err := SubstituteParams(map[string]any{"x": "{{.entity.dns.fqdn}}"}, ns); err == nil {
		t.Fatal("with NO deferral, entity must be an unknown namespace")
	}

	// Second stage: the deferred token resolves against the namespace it waited for, and keeps
	// ADR-0024's type preservation.
	stage2 := Namespaces{"entity": {"dns": map[string]any{"fqdn": "web-1.stratt.test"}, "name": "web-1"}}
	final, err := SubstituteParams(map[string]any{"commonName": got["commonName"]}, stage2)
	if err != nil {
		t.Fatalf("second stage: %v", err)
	}
	if final["commonName"] != "web-1.stratt.test" {
		t.Fatalf("the deferred token must resolve in stage two, got %q", final["commonName"])
	}
}
