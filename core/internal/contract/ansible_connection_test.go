package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

// ansible.input.v6 (ADR-0126 D1) moves the connection keys out of extraVars and into a
// typed `connection` block. This is the guard that makes the move real rather than
// advisory: while `extraVars` accepted them, the credential a Step was AUTHORIZED to use
// and the key file it actually read were two facts nothing reconciled (§2.4) — and every
// Workflow in the repo carried the literal path, so the drift was the norm.
//
// Refused, not merged: a merge needs a winner, and a silent winner between the two is the
// same defect restated.
func TestV6RefusesConnectionKeysInExtraVars(t *testing.T) {
	for _, key := range []string{"ansible_user", "ansible_ssh_private_key_file", "ansible_ssh_common_args"} {
		doc := json.RawMessage(`{"play":"- hosts: all","extraVars":{"` + key + `":"x"}}`)
		err := ValidateActuatorParams("ansible", doc)
		if err == nil {
			t.Errorf("extraVars.%s must be refused — it is the shim's to render", key)
			continue
		}
		// §1.8: the refusal has to name the offending key, or an operator holding a
		// 40-line extraVars block has no idea which one moved.
		if !strings.Contains(err.Error(), key) {
			t.Errorf("the refusal must name %s, got %v", key, err)
		}
	}
}

// The positive half: a full v6 connection block validates. Without this the guard above
// could pass simply by refusing everything.
func TestV6AcceptsTheConnectionBlock(t *testing.T) {
	doc := json.RawMessage(`{
		"play":"- hosts: all",
		"connection":{"user":"appops","credentialRef":"node-key","file":"id_ed25519",
		              "hostKeyChecking":"accept-new"},
		"extraVars":{"app_port":"443"}
	}`)
	if err := ValidateActuatorParams("ansible", doc); err != nil {
		t.Fatalf("a well-formed v6 connection must validate: %v", err)
	}
}

// The jump chain (ADR-0126 D3) landed WITH its renderer — plugins/ansible.proxyJump —
// which is the only condition under which a Contract field should exist at all: an
// accepted-but-unconsumed field is indistinguishable from a working one until someone
// relies on it. The predecessor of this test asserted its ABSENCE for exactly one
// commit, and failing that test is what marked the renderer's arrival.
func TestV6DeclaresTheJumpChainAuth(t *testing.T) {
	ok := json.RawMessage(`{"play":"- hosts: all","connection":{"jump":[{"user":"jump","credentialRef":"bastion-key"}]}}`)
	if err := ValidateActuatorParams("ansible", ok); err != nil {
		t.Fatalf("per-hop jump auth must validate: %v", err)
	}
	// No ADDRESS field: a hop's coordinate comes from its Entity's mgmt.address, and
	// accepting one here would create the second home §2.4 forbids.
	bad := json.RawMessage(`{"play":"- hosts: all","connection":{"jump":[{"address":"10.0.0.9"}]}}`)
	if err := ValidateActuatorParams("ansible", bad); err == nil {
		t.Fatal("a hop ADDRESS must be refused — the graph owns the topology, not the Step")
	}
}

// hostKeyChecking is an enum, so a typo cannot silently become "whatever the tool
// defaults to" — the failure mode that makes a security default worthless.
func TestV6HostKeyCheckingIsAClosedSet(t *testing.T) {
	doc := json.RawMessage(`{"play":"- hosts: all","connection":{"hostKeyChecking":"no"}}`)
	if err := ValidateActuatorParams("ansible", doc); err == nil {
		t.Fatal(`hostKeyChecking:"no" must be refused — the values are strict|accept-new|off`)
	}
}

// v6 is ADDITIVE over v5 (the registry keeps one live actuators/ansible.input — the
// highest version — and a Step cannot pin one), so every v5 declaration must still
// validate unchanged. A regression here silently breaks every shipped Workflow.
func TestV6IsAdditiveOverV5(t *testing.T) {
	v5 := json.RawMessage(`{
		"play":"- hosts: all","become":{"enabled":true,"method":"su","user":"root"},
		"limit":"web*","tags":["a"],"skipTags":["b"],"forks":5,"diff":true,
		"verbosity":2,"timeout":30,"vault":{"credentialRef":"v","file":"pw"},
		"extraVars":{"app_port":"443"}
	}`)
	if err := ValidateActuatorParams("ansible", v5); err != nil {
		t.Fatalf("every v5 field must still validate under v6: %v", err)
	}
}
