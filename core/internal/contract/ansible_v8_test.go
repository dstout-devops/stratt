package contract

import (
	"strings"
	"testing"
)

// ADR-0153 · the reach gap. v8 adds a connection TYPE and the three credential FORMS the
// connection surface had no shape for. These assert the CONTRACT — the shim's own tests
// assert the rendering — because the Contract is the half an estate author hits first, and
// a value it accepts is a promise the shim then has to keep.
func TestAnsibleV8ConnectionTypeAndCredentialForms(t *testing.T) {
	good := []string{
		// The value this ADR exists for.
		`{"playbook":"site.yml","connection":{"type":"network_cli","networkOS":"cisco.ios.ios","user":"netops"}}`,
		`{"playbook":"site.yml","connection":{"type":"netconf","networkOS":"junipernetworks.junos.junos"}}`,
		// A device password, brokered.
		`{"playbook":"s.yml","connection":{"type":"network_cli","networkOS":"arista.eos.eos","passwordRef":{"credentialRef":"dev-pw"}}}`,
		// ANS-010 — an escalation password.
		`{"playbook":"s.yml","become":{"enabled":true,"passwordRef":{"credentialRef":"sudo-pw","file":"pw"}}}`,
		// ANS-011 — one identity, and many.
		`{"playbook":"s.yml","vault":{"credentialRef":"v"}}`,
		`{"playbook":"s.yml","vault":[{"credentialRef":"v"}]}`,
		`{"playbook":"s.yml","vault":[{"credentialRef":"a","id":"prod"},{"credentialRef":"b","id":"legacy"}]}`,
	}
	for _, doc := range good {
		if err := ValidateActuatorParams("ansible", []byte(doc)); err != nil {
			t.Errorf("v8 must accept %s\n  %v", doc, err)
		}
	}

	bad := map[string]string{
		// Windows: still no, and the Contract is where an estate author must learn it —
		// not a Run that dies on a fleet somebody already migrated.
		"winrm":                      `{"playbook":"s.yml","connection":{"type":"winrm"}}`,
		"psrp":                       `{"playbook":"s.yml","connection":{"type":"psrp"}}`,
		"httpapi (its own decision)": `{"playbook":"s.yml","connection":{"type":"httpapi"}}`,
		"docker":                     `{"playbook":"s.yml","connection":{"type":"docker"}}`,
		// `local` is a property of the TARGET (mgmt.address's reserved value). A second
		// home for it would be resolved by ansible's host-vars-beat-group-vars rule.
		"local as a params value": `{"playbook":"s.yml","connection":{"type":"local"}}`,
		// The password is a REF, never a value — there is no shape here that takes one.
		"an inline password":        `{"playbook":"s.yml","connection":{"password":"hunter2"}}`,
		"an inline become password": `{"playbook":"s.yml","become":{"enabled":true,"password":"hunter2"}}`,
		"a passwordRef with no ref": `{"playbook":"s.yml","connection":{"passwordRef":{"file":"pw"}}}`,
		"an unknown passwordRef field": `{"playbook":"s.yml","connection":{"passwordRef":` +
			`{"credentialRef":"a","path":"/etc/x"}}}`,
		// The netcommon os is bounded — it reaches a connection plugin by name.
		"a networkOS with a slash": `{"playbook":"s.yml","connection":{"type":"network_cli","networkOS":"cisco/ios"}}`,
		// An empty vault list declares nothing while looking configured.
		"an empty vault list":       `{"playbook":"s.yml","vault":[]}`,
		"a vault entry with no ref": `{"playbook":"s.yml","vault":[{"id":"prod"}]}`,
	}
	for name, doc := range bad {
		if err := ValidateActuatorParams("ansible", []byte(doc)); err == nil {
			t.Errorf("v8 must REFUSE %s: %s", name, doc)
		}
	}
}

// THE COMPATIBILITY PROMISE, and it is load-bearing rather than polite: the registry keeps
// one live actuators/ansible.input and a Step CANNOT pin a version (ADR-0132 D4). A v8 that
// dropped or reshaped a v7 field would fail every shipped declaration the moment it landed.
func TestAnsibleV8KeepsEveryV7ShapeValid(t *testing.T) {
	v7Shapes := []string{
		`{"play":"- hosts: all\n"}`,
		`{"scm":{"repo":"https://x/r.git","playbook":"site.yml"},"extraVars":{"a":1}}`,
		`{"playbook":"playbooks/web/deploy.yml"}`,
		`{"playbook":"s.yml","become":{"enabled":true,"user":"root","method":"sudo"}}`,
		`{"playbook":"s.yml","connection":{"user":"appops","credentialRef":"k","hostKeyChecking":"strict"}}`,
		`{"playbook":"s.yml","connection":{"jump":[{"user":"bastion","credentialRef":"b"}]}}`,
		// The OBJECT vault form — the one an array-only v8 would have broken.
		`{"playbook":"s.yml","vault":{"credentialRef":"v","file":"pw"}}`,
		`{"playbook":"s.yml","limit":"web*","tags":["a"],"forks":10,"verbosity":2,"timeout":30}`,
	}
	for _, doc := range v7Shapes {
		if err := ValidateActuatorParams("ansible", []byte(doc)); err != nil {
			t.Errorf("v8 broke a v7 declaration — a Step cannot pin a version, so this fails an "+
				"estate on upgrade: %s\n  %v", doc, err)
		}
	}
}

// v6's refusal must survive: connection keys belong in `connection`, never in extraVars,
// because a merge between the two needs a winner (ADR-0126 D1). v8 adds a password to the
// same block and must not reopen the door beside it.
func TestAnsibleV8StillRefusesConnectionKeysInExtraVars(t *testing.T) {
	for _, key := range []string{"ansible_user", "ansible_ssh_private_key_file", "ansible_ssh_common_args"} {
		doc := `{"playbook":"s.yml","extraVars":{"` + key + `":"x"}}`
		if err := ValidateActuatorParams("ansible", []byte(doc)); err == nil {
			t.Errorf("extraVars must still refuse %s", key)
		} else if !strings.Contains(err.Error(), key) {
			t.Errorf("the refusal should name the offending key, got %v", err)
		}
	}
}
