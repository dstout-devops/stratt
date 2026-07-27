package contract

import "testing"

// The shape gate's own unit coverage. The estate tests prove it fires on the real defect
// (PRV-1); these prove it fires for the right REASON and, more importantly, that it stays
// narrow — a shape gate that over-refuses blocks estates the launch path would accept.
func TestCheckKeys(t *testing.T) {
	closed := []byte(`{
		"type":"object",
		"properties":{"region":{"type":"string"},"ami":{"type":"string"}},
		"required":["region","ami"],
		"additionalProperties":false
	}`)
	open := []byte(`{
		"type":"object",
		"properties":{"region":{"type":"string"}},
		"additionalProperties":true
	}`)
	// additionalProperties as a SCHEMA, not a boolean — legal JSON Schema, and it does not
	// close the set. Treating any non-empty value as "closed" would refuse this wrongly.
	schemaAdditional := []byte(`{
		"type":"object",
		"properties":{"region":{"type":"string"}},
		"additionalProperties":{"type":"string"}
	}`)

	t.Run("unknown key on a closed schema is refused", func(t *testing.T) {
		err := checkKeys("c", closed, map[string]any{
			"region": "{{.launch.params.region}}", "ami": "x", "subnet": "{{.launch.placement.subnet}}",
		})
		if err == nil {
			t.Fatal("an undeclared key must be refused — this is the PRV-1 catch")
		}
		// The message has to name the offending key AND what is accepted, or the author
		// cannot act on it (§1.8).
		for _, want := range []string{`"subnet"`, "additionalProperties:false", "ami"} {
			if !contains(err.Error(), want) {
				t.Errorf("message must contain %q, got: %v", want, err)
			}
		}
	})

	t.Run("templated values never matter", func(t *testing.T) {
		if err := checkKeys("c", closed, map[string]any{
			"region": "{{.launch.params.region}}", "ami": "{{.launch.params.ami}}",
		}); err != nil {
			t.Fatalf("a declared key with a templated value must pass — values are launch's job: %v", err)
		}
	})

	t.Run("an open schema accepts anything", func(t *testing.T) {
		if err := checkKeys("c", open, map[string]any{"region": "x", "anything": "y"}); err != nil {
			t.Fatalf("additionalProperties:true is a promise this gate must honour: %v", err)
		}
	})

	t.Run("additionalProperties as a schema does not close the set", func(t *testing.T) {
		if err := checkKeys("c", schemaAdditional, map[string]any{"region": "x", "extra": "y"}); err != nil {
			t.Fatalf("only the literal false closes a property set: %v", err)
		}
	})

	t.Run("a required key written nowhere is refused", func(t *testing.T) {
		if err := checkKeys("c", closed, map[string]any{"region": "x"}); err == nil {
			t.Fatal("no template can supply a key that is absent from the declaration")
		}
	})

	t.Run("a required key present but templated is satisfied", func(t *testing.T) {
		if err := checkKeys("c", closed, map[string]any{
			"region": "{{.launch.params.region}}", "ami": "{{.launch.params.ami}}",
		}); err != nil {
			t.Fatalf("presence satisfies required; resolution is launch's job: %v", err)
		}
	})

	t.Run("an unparseable or empty schema is not this gate's error to raise", func(t *testing.T) {
		if err := checkKeys("c", []byte(`{oh no`), map[string]any{"x": 1}); err != nil {
			t.Fatalf("the compiler owns malformed-schema errors, not the shape gate: %v", err)
		}
		if err := checkKeys("c", nil, map[string]any{"x": 1}); err != nil {
			t.Fatalf("no schema, no opinion: %v", err)
		}
	})
}

// ValidateParamKeys must stay silent about a contract that does not exist — the caller checks
// existence and can name it better. Otherwise every Step naming an unregistered plugin Action
// would get two errors describing one problem.
func TestValidateParamKeysUnknownContractIsSilent(t *testing.T) {
	if err := ValidateParamKeys("actions/nope/nothing.input", map[string]any{"a": 1}); err != nil {
		t.Fatalf("existence is the caller's check: %v", err)
	}
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && (len(needle) == 0 || indexOf(hay, needle) >= 0)
}

func indexOf(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
