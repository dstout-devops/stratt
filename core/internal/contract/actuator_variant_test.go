package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

// An input Contract belongs to the TOOL, not to the local name an estate gives one of
// its Actuators. Resolution by name alone made per-Step EE selection (ADR-0117 D3a)
// unreachable from an estate: the dispatcher carried the declared image, but declaring
// a second ansible Actuator to select a content-bearing EE produced an Actuator whose
// every Step was rejected at parse time as uncontracted. The mechanism shipped; the
// thing it existed for did not. The app-cert demo is what surfaced it.
func TestActuatorVariantResolvesItsPluginsContract(t *testing.T) {
	params := json.RawMessage(`{"play":"- hosts: all\n  tasks: []\n"}`)

	// The variant name has no Contract of its own, and must resolve through its
	// plugin identity.
	if err := ValidateActuatorParamsFor("ansible-crypto", "ansible", params); err != nil {
		t.Fatalf("an Actuator variant must validate against its plugin's Contract: %v", err)
	}

	// Without the identity, the same declaration is uncontracted — the pre-fix
	// behaviour, kept as the explicit contrast so the fallback cannot be quietly
	// widened into "anything validates".
	if err := ValidateActuatorParams("ansible-crypto", params); err == nil {
		t.Fatal("an unknown Actuator with no identity must still be refused (§2.3)")
	}

	// The name still wins when it has a Contract: the identity is a fallback, never
	// an override.
	if err := ValidateActuatorParamsFor("ansible", "ansible", params); err != nil {
		t.Fatalf("a boot-registered Actuator must keep validating by name: %v", err)
	}

	// A variant of a plugin that has no Contract at all is refused, and the error
	// names BOTH keys it looked under — §1.8, an operator must not have to guess
	// which of the two names was wrong.
	err := ValidateActuatorParamsFor("mystery-variant", "mystery", params)
	if err == nil {
		t.Fatal("a variant of an uncontracted plugin must be refused")
	}
	for _, want := range []string{"mystery-variant", "mystery"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name %q so the fix is obvious; got: %v", want, err)
		}
	}
}

// The variant must be held to the SAME schema as its plugin — the fallback resolves
// which Contract applies, it does not relax it.
func TestActuatorVariantIsHeldToTheSameSchema(t *testing.T) {
	bad := json.RawMessage(`{"play":"- hosts: all\n","become":{"method":"definitely-not-a-method"}}`)
	err := ValidateActuatorParamsFor("ansible-crypto", "ansible", bad)
	if err == nil {
		t.Fatal("an Actuator variant must be held to its plugin's Contract, not exempted from it")
	}
	if !strings.Contains(err.Error(), "actuators/ansible.input") {
		t.Errorf("the violation must name the plugin's Contract: %v", err)
	}
}
