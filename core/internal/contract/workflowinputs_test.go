package contract

import (
	"encoding/json"
	"fmt"
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

// ── ResolveLaunchInputs: the chokepoint every transport passes through ────────────────

func resolve(t *testing.T, schema string, supplied map[string]any) (map[string]any, error) {
	t.Helper()
	var raw json.RawMessage
	if schema != "" {
		raw = doc(t, schema)
	}
	return ResolveLaunchInputs("w", raw, supplied)
}

const portSchema = `{
	"type": "object",
	"additionalProperties": false,
	"required": ["commonName"],
	"properties": {
		"commonName": {"type": "string"},
		"tlsPort": {"type": "integer", "default": 443}
	}
}`

// Resolved numbers come back as json.Number, NOT float64 — deliberately. The canonicalizing
// pass preserves integer precision rather than rounding through a float64, which is the same
// hazard ADR-0117 D6 hit when a big integer in a module result voided a whole event. A
// json.Number marshals back to an unquoted JSON number, so the wire form is unchanged.
func TestDefaultsAreApplied(t *testing.T) {
	got, err := resolve(t, portSchema, map[string]any{"commonName": "web.test"})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got["tlsPort"]) != "443" {
		t.Fatalf("an unsupplied input must take its declared default, got %#v", got["tlsPort"])
	}
	if _, ok := got["tlsPort"].(json.Number); !ok {
		t.Errorf("numbers must stay json.Number to preserve integer precision, got %T", got["tlsPort"])
	}
}

// TestLargeIntegerPrecisionSurvives: the reason for json.Number, made explicit. A float64
// cannot hold 2^53+1 exactly, so a value round-tripped through one comes back WRONG — the
// ADR-0117 D6 defect shape, at the launch boundary this time.
func TestLargeIntegerPrecisionSurvives(t *testing.T) {
	got, err := resolve(t, `{"type":"object","additionalProperties":false,`+
		`"properties":{"n":{"type":"integer"}}}`, map[string]any{"n": json.Number("9007199254740993")})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got["n"]) != "9007199254740993" {
		t.Fatalf("a large integer must survive resolution exactly, got %v", got["n"])
	}
}

func TestSuppliedValueBeatsTheDefault(t *testing.T) {
	got, err := resolve(t, portSchema, map[string]any{"commonName": "web.test", "tlsPort": 8443})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got["tlsPort"]) != "8443" {
		t.Fatalf("a supplied value must stand, got %#v", got["tlsPort"])
	}
}

// TestExplicitNullIsNotDefaulted: a caller passing null is STATING null. Overwriting it
// with a default would be a silent override of an explicit value — the same class of
// implicit resolution §2.4 rejects, one layer down.
func TestExplicitNullIsNotDefaulted(t *testing.T) {
	_, err := resolve(t, portSchema, map[string]any{"commonName": "web.test", "tlsPort": nil})
	if err == nil {
		t.Fatal("explicit null must be validated as null (and fail an integer input), not replaced by the default")
	}
}

func TestMissingRequiredInputRejected(t *testing.T) {
	if _, err := resolve(t, portSchema, map[string]any{"tlsPort": 8443}); err == nil {
		t.Fatal("a missing required input must be rejected")
	}
}

// TestUnknownInputRejected is the property ADR-0118 D2 promised and that only became
// enforceable once change context moved to its own field (D4): while both shared one bag, a
// policy-gated Workflow's `environment` was indistinguishable from a stray parameter.
func TestUnknownInputRejected(t *testing.T) {
	_, err := resolve(t, portSchema, map[string]any{"commonName": "web.test", "tlsPrt": 8443})
	if err == nil {
		t.Fatal("an unknown input must be rejected, not silently dropped")
	}
	if !strings.Contains(err.Error(), "tlsPrt") {
		t.Errorf("the error should name the offending key; got: %v", err)
	}
}

func TestWrongTypeRejected(t *testing.T) {
	if _, err := resolve(t, portSchema, map[string]any{"commonName": "web.test", "tlsPort": "8443"}); err == nil {
		t.Fatal(`"8443" must not satisfy an integer input — no coercion`)
	}
}

// TestNoSchemaWithParamsRejectedAndPointsAtContext: the error has to teach the split,
// because "my params were rejected" is otherwise a dead end for someone who was passing
// policy context.
func TestNoSchemaWithParamsRejectedAndPointsAtContext(t *testing.T) {
	_, err := resolve(t, "", map[string]any{"environment": "prod"})
	if err == nil {
		t.Fatal("a Workflow declaring no inputs must reject supplied inputs")
	}
	if !strings.Contains(err.Error(), "context") {
		t.Errorf("the error must point at `context` for change facts; got: %v", err)
	}
}

func TestNoSchemaNoParamsIsFine(t *testing.T) {
	got, err := resolve(t, "", nil)
	if err != nil || got != nil {
		t.Fatalf("a Workflow that takes nothing, given nothing, resolves to nothing: %v %v", got, err)
	}
}
