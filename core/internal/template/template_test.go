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

// No operators / expression syntax: a token with an operator is not a valid
// token and is left as literal text (never evaluated) — the non-goal guard.
func TestNotAnExpressionLanguage(t *testing.T) {
	for _, s := range []string{"{{.event.a + .event.b}}", "{{ len(.event.x) }}", "{{.event.a == 1}}"} {
		got, err := Substitute(s, ns())
		if err != nil || got != s {
			t.Fatalf("expression-like token must pass through as literal, not evaluate: %q → %v err=%v", s, got, err)
		}
	}
}
