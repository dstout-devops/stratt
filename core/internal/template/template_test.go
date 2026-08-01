package template

import (
	"reflect"
	"testing"
)

func ns() Namespaces {
	return Namespaces{
		"spec":  {"package": "google-chrome", "count": float64(3)},
		"event": {"labels": map[string]any{"instance": "web-01", "severity": "critical"}, "value": true},
	}
}

func TestSubstituteTypePreservation(t *testing.T) {
	// Exact single token → native type preserved.
	got, err := Substitute("{{.spec.count}}", ns())
	if err != nil || got != float64(3) {
		t.Fatalf("exact token must keep number type: %v (%T) err=%v", got, got, err)
	}
	got, _ = Substitute("{{.event.value}}", ns())
	if got != true {
		t.Fatalf("bool preserved: %v", got)
	}
	// Nested object preserved.
	got, _ = Substitute("{{.event.labels}}", ns())
	if m, ok := got.(map[string]any); !ok || m["instance"] != "web-01" {
		t.Fatalf("object preserved: %v", got)
	}
}

func TestSubstituteEmbedded(t *testing.T) {
	// Embedded token → rendered into surrounding text (string result).
	got, err := Substitute("host-{{.event.labels.instance}}-x", ns())
	if err != nil || got != "host-web-01-x" {
		t.Fatalf("embedded: %v err=%v", got, err)
	}
	// Dotted path into nested maps.
	got, _ = Substitute("{{.event.labels.severity}}", ns())
	if got != "critical" {
		t.Fatalf("dotted path: %v", got)
	}
}

func TestSubstituteWalksStructure(t *testing.T) {
	in := map[string]any{
		"script":  "echo {{.event.labels.instance}}",
		"count":   "{{.spec.count}}",
		"nested":  []any{"{{.spec.package}}", "literal"},
		"literal": 42,
	}
	out, err := SubstituteParams(in, ns())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"script":  "echo web-01",
		"count":   float64(3),
		"nested":  []any{"google-chrome", "literal"},
		"literal": 42,
	}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("walk: %#v", out)
	}
}

func TestSubstituteFailClosed(t *testing.T) {
	if _, err := Substitute("{{.event.labels.missing}}", ns()); err == nil {
		t.Fatal("unknown field must error")
	}
	if _, err := Substitute("{{.nope.x}}", ns()); err == nil {
		t.Fatal("unknown namespace must error")
	}
	// No token passes through untouched.
	got, err := Substitute("plain string", ns())
	if err != nil || got != "plain string" {
		t.Fatalf("passthrough: %v %v", got, err)
	}
}

func TestHasAndReferences(t *testing.T) {
	if !Has(map[string]any{"a": []any{"{{.event.x}}"}}) {
		t.Fatal("Has must recurse")
	}
	if Has(map[string]any{"a": "plain"}) {
		t.Fatal("no token")
	}
	refs := References(map[string]any{"a": "{{.event.x}}", "b": "{{.param.y}} and {{.spec.z}}"})
	if !refs["event"] || !refs["param"] || !refs["spec"] {
		t.Fatalf("references: %v", refs)
	}
}

// No operators / expression syntax — the non-goal guard (§1, "no new configuration languages").
//
// AMENDS ADR-0024 D1's stated behaviour, on charter-guardian's ruling (2026-07-30). D1 said an
// expression-like token "passes through as literal text, never evaluated", and this test asserted
// the pass-through. It is now REFUSED instead. Not evaluating it and silently accepting it as a
// VALUE are different things, and ADR-0150 D2 is what turned the difference into a hazard: a
// `commonName` that resolves to the literal "{{.entity.dns.fqdn | lower}}" is a perfectly good
// string, satisfies its Contract, and gets issued as a CERTIFICATE SUBJECT. The non-goal is still
// held — nothing is evaluated, ever — and it is now held by the grammar rather than by hoping
// nobody writes one.
func TestNotAnExpressionLanguage(t *testing.T) {
	for _, s := range []string{"{{.event.a + .event.b}}", "{{ len(.event.x) }}", "{{.event.a == 1}}", "{{.event}}", "{{.event.a}"} {
		got, err := Substitute(s, ns())
		if err == nil {
			t.Fatalf("expression-like or malformed token must be REFUSED, not accepted as a literal value: %q → %v", s, got)
		}
	}
	// Text that merely CONTAINS braces is untouched — the guard fires on `{{`, not on punctuation.
	if got, err := Substitute("a { literal } value", ns()); err != nil || got != "a { literal } value" {
		t.Fatalf("ordinary braces must pass through: %v err=%v", got, err)
	}
}
