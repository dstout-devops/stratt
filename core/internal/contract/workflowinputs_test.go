package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

// These cover the launch boundary's declaration-time gate (ADR-0118 D2). The seam is
// supply-chain-adjacent in the same way the EE pin gate is: it is the only thing standing
// between a Workflow that CLAIMS a typed interface and one that actually has a usable one,
// and an operator who believes the door is checked when it is not is worse off than one who
// knows it isn't (§1.8).

func doc(t *testing.T, s string) json.RawMessage {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("bad test fixture: %v", err)
	}
	return json.RawMessage(s)
}

func TestNoInputsIsLegal(t *testing.T) {
	sch, err := CompileInputSchema("w", nil)
	if err != nil || sch != nil {
		t.Fatalf("a Workflow taking nothing must be legal, got schema=%v err=%v", sch, err)
	}
}

func TestValidClosedObjectSchemaCompiles(t *testing.T) {
	sch, err := CompileInputSchema("w", doc(t, `{
		"type": "object",
		"additionalProperties": false,
		"required": ["commonName"],
		"properties": {
			"commonName": {"type": "string"},
			"tlsPort": {"type": "integer", "default": 443}
		}
	}`))
	if err != nil {
		t.Fatalf("a valid closed object schema must compile: %v", err)
	}
	if sch == nil {
		t.Fatal("expected a compiled schema")
	}
	// The compiled schema is the real thing, not a stub: it enforces its own rules.
	if err := sch.Validate(map[string]any{"commonName": "web.test"}); err != nil {
		t.Fatalf("a valid instance must pass: %v", err)
	}
	if err := sch.Validate(map[string]any{}); err == nil {
		t.Fatal("a missing required input must fail")
	}
	if err := sch.Validate(map[string]any{"commonName": "w", "nope": 1}); err == nil {
		t.Fatal("additionalProperties: false must reject an unknown key")
	}
	// F9's coercion vector: a string where an integer is declared must FAIL, not coerce.
	// Silent coercion would be evaluation semantics entering by the back door.
	if err := sch.Validate(map[string]any{"commonName": "w", "tlsPort": "443"}); err == nil {
		t.Fatal(`"443" must not satisfy an integer input — coercion is forbidden, not lenient`)
	}
}

// TestNonObjectSchemaRejected: launch params are a key/value body, so a schema for any
// other type describes something no launch could ever satisfy.
func TestNonObjectSchemaRejected(t *testing.T) {
	for _, bad := range []string{
		`{"type": "string"}`,
		`{"type": "array", "items": {"type": "string"}}`,
		`{"additionalProperties": false, "properties": {}}`, // no type at all
	} {
		if _, err := CompileInputSchema("w", doc(t, bad)); err == nil {
			t.Fatalf("must require type: object, accepted: %s", bad)
		}
	}
}

// TestOpenSchemaRejected is the half-declaration rule applied to this seam: an input schema
// that does not close itself cannot reject a typo at launch, so it silently accepts one —
// the same defect shape as a declared port with no address (ADR-0117 D5a).
func TestOpenSchemaRejected(t *testing.T) {
	for _, bad := range []string{
		`{"type": "object", "properties": {"x": {"type": "string"}}}`,                               // omitted
		`{"type": "object", "additionalProperties": true, "properties": {"x": {"type": "string"}}}`, // explicit true
		`{"type": "object", "additionalProperties": {"type": "string"}, "properties": {}}`,          // schema-valued
	} {
		_, err := CompileInputSchema("w", doc(t, bad))
		if err == nil {
			t.Fatalf("must require additionalProperties: false, accepted: %s", bad)
		}
		if !strings.Contains(err.Error(), "additionalProperties") {
			t.Errorf("the error must name the missing keyword; got: %v", err)
		}
	}
}

// TestDefaultViolatingItsOwnSchemaRejected: a default that cannot satisfy the type it is
// declared under is a lying declaration. Caught here rather than surfacing later as a
// baffling launch failure on a value nobody supplied.
func TestDefaultViolatingItsOwnSchemaRejected(t *testing.T) {
	_, err := CompileInputSchema("w", doc(t, `{
		"type": "object",
		"additionalProperties": false,
		"properties": {"tlsPort": {"type": "integer", "default": "443"}}
	}`))
	if err == nil {
		t.Fatal(`a string default under "type": "integer" must be rejected`)
	}
	if !strings.Contains(err.Error(), "tlsPort") {
		t.Errorf("the error must name the offending property; got: %v", err)
	}
}

func TestValidDefaultAccepted(t *testing.T) {
	if _, err := CompileInputSchema("w", doc(t, `{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"tlsPort": {"type": "integer", "default": 443},
			"channel": {"type": "string", "enum": ["stable", "beta"], "default": "stable"}
		}
	}`)); err != nil {
		t.Fatalf("defaults satisfying their own schemas must be accepted: %v", err)
	}
}

// TestDefaultViolatingAnEnumRejected: the check is the property's WHOLE subschema, not just
// its `type` — an enum is exactly the case a type-only check would miss.
func TestDefaultViolatingAnEnumRejected(t *testing.T) {
	if _, err := CompileInputSchema("w", doc(t, `{
		"type": "object",
		"additionalProperties": false,
		"properties": {"channel": {"type": "string", "enum": ["stable", "beta"], "default": "nightly"}}
	}`)); err == nil {
		t.Fatal("a default outside its enum must be rejected")
	}
}

func TestMalformedSchemaRejected(t *testing.T) {
	// Valid JSON, invalid JSON Schema (properties must be an object of schemas).
	if _, err := CompileInputSchema("w", json.RawMessage(`{"type":"object","additionalProperties":false,"properties":[1,2]}`)); err == nil {
		t.Fatal("a malformed schema must be rejected at declaration")
	}
	// Not JSON at all.
	if _, err := CompileInputSchema("w", json.RawMessage(`{nope`)); err == nil {
		t.Fatal("non-JSON inputs must be rejected")
	}
}

func TestInputNames(t *testing.T) {
	names, err := InputNames(doc(t, `{
		"type": "object", "additionalProperties": false,
		"properties": {"a": {"type": "string"}, "b": {"type": "integer"}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || !names["a"] || !names["b"] {
		t.Fatalf("declared names: %v", names)
	}
	if got, err := InputNames(nil); err != nil || got != nil {
		t.Fatalf("no inputs ⇒ no names, got %v %v", got, err)
	}
}
