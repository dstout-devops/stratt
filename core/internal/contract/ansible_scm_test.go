package contract

import "testing"

// TestAnsibleHighestVersionResolved locks that the highest-versioned sibling
// wins the actuators/ansible.input lookup (path-sorted load) — currently v8
// (the connection type + the credential forms, ADR-0153), which still carries
// v7's mounted-project `playbook`, v6's connection block, the v3 scm content-ref
// and the v4 extraVars: every version has been additive, since the registry keeps
// exactly ONE live actuators/ansible.input and a Step cannot pin a version.
func TestAnsibleHighestVersionResolved(t *testing.T) {
	c, ok, err := Get("actuators/ansible.input")
	if err != nil || !ok {
		t.Fatalf("ansible.input contract: ok=%v err=%v", ok, err)
	}
	if c.Version != 9 {
		t.Fatalf("resolved ansible.input version = %d, want 9 (the kubectl reach credential, ADR-0156 D4a)", c.Version)
	}
}

// TestAnsibleV7PlaybookValidates pins the v7 seam (ADR-0134 D4). Two halves, and the
// second is the one that matters: `playbook` is accepted alongside every other field, and
// it is MUTUALLY EXCLUSIVE with the other two content sources — project/ has room for one
// of inline, cloned and mounted, and a merge between them would need a winner.
func TestAnsibleV7PlaybookValidates(t *testing.T) {
	good := []string{
		`{"playbook":"site.yml"}`,
		`{"playbook":"playbooks/web/deploy.yml"}`,
		`{"playbook":"site.yml","extraVars":{"x":"1"},"become":{"enabled":true}}`,
	}
	for _, p := range good {
		if err := ValidateActuatorParams("ansible", []byte(p)); err != nil {
			t.Errorf("valid v7 params rejected: %s: %v", p, err)
		}
	}
	bad := map[string]string{
		// The path is segment-bounded rather than lookahead-guarded, so traversal is
		// unrepresentable rather than filtered.
		"traversal":               `{"playbook":"../../etc/passwd"}`,
		"absolute":                `{"playbook":"/etc/passwd"}`,
		"leading dash":            `{"playbook":"-oProxyCommand=x"}`,
		"empty":                   `{"playbook":""}`,
		"playbook and play":       `{"playbook":"site.yml","play":"- hosts: all"}`,
		"playbook and scm":        `{"playbook":"site.yml","scm":{"repo":"r","playbook":"p"}}`,
		"play and scm still":      `{"play":"- hosts: all","scm":{"repo":"r","playbook":"p"}}`,
		"segment starting with .": `{"playbook":".hidden/site.yml"}`,
	}
	for name, p := range bad {
		if err := ValidateActuatorParams("ansible", []byte(p)); err == nil {
			t.Errorf("%s: expected rejection, got nil", name)
		}
	}
}

func TestAnsibleSCMParamsValidate(t *testing.T) {
	// A valid content-ref.
	if err := ValidateActuatorParams("ansible", []byte(`{"scm":{"repo":"https://x/r.git","ref":"main","playbook":"site.yml"}}`)); err != nil {
		t.Fatalf("valid scm content-ref rejected: %v", err)
	}
	// scm requires repo + playbook.
	if err := ValidateActuatorParams("ansible", []byte(`{"scm":{"ref":"main"}}`)); err == nil {
		t.Fatal("scm without repo/playbook must be rejected")
	}
	// play and scm are mutually exclusive.
	if err := ValidateActuatorParams("ansible", []byte(`{"play":"- hosts: all\n","scm":{"repo":"r","playbook":"p"}}`)); err == nil {
		t.Fatal("play and scm together must be rejected")
	}
	// The v2 fields still validate (no regression).
	if err := ValidateActuatorParams("ansible", []byte(`{"play":"- hosts: all\n","check":true}`)); err != nil {
		t.Fatalf("v2 params regressed: %v", err)
	}
	// v4 extraVars validates alongside play or scm.
	if err := ValidateActuatorParams("ansible", []byte(`{"play":"- hosts: all\n","extraVars":{"msg":"hi"}}`)); err != nil {
		t.Fatalf("extraVars rejected: %v", err)
	}
	if err := ValidateActuatorParams("ansible", []byte(`{"scm":{"repo":"r","playbook":"p"},"extraVars":{"x":1}}`)); err != nil {
		t.Fatalf("scm+extraVars rejected: %v", err)
	}
}

// TestAnsibleV5RunKnobsValidate pins the v5 typed run knobs (ADR-0117 D1): valid
// values pass, and every bounded field REJECTS the value that would make it an
// injection seam. This is the test that earns the "typed, not a passthrough"
// argument — if these bounds regress, the Contract stops being a boundary.
func TestAnsibleV5RunKnobsValidate(t *testing.T) {
	ok := `{"play":"- hosts: all","become":{"enabled":true,"user":"deploy","method":"sudo"},
	        "limit":"web-01","tags":["install"],"skipTags":["slow"],"forks":10,
	        "diff":true,"verbosity":2,"timeout":30,"vault":{"credentialRef":"vault-dev"}}`
	if err := ValidateActuatorParams("ansible", []byte(ok)); err != nil {
		t.Fatalf("valid v5 knobs rejected: %v", err)
	}

	bad := map[string]string{
		"become.method open string":   `{"become":{"method":"sudo; id"}}`,
		"become.user metacharacters":  `{"become":{"user":"root; id"}}`,
		"limit shell metacharacters":  `{"limit":"web-01;id"}`,
		"limit command substitution":  `{"limit":"$(id)"}`,
		"limit whitespace":            `{"limit":"a b"}`,
		"tag with a space":            `{"tags":["a b"]}`,
		"forks out of range":          `{"forks":100000}`,
		"verbosity out of range":      `{"verbosity":9}`,
		"vault without credentialRef": `{"vault":{"file":"pw"}}`,
		"unknown passthrough field":   `{"cmdline":"--become"}`,
	}
	for name, params := range bad {
		if err := ValidateActuatorParams("ansible", []byte(params)); err == nil {
			t.Errorf("%s: expected rejection, got nil", name)
		}
	}

	// The deprecated fields still VALIDATE (retained so nothing breaks, ADR-0117
	// D2/D3a) — they are simply never read by the shim.
	for _, p := range []string{`{"check":true}`, `{"eeImage":"stratt-ee:dev"}`} {
		if err := ValidateActuatorParams("ansible", []byte(p)); err != nil {
			t.Errorf("deprecated field must still validate: %s: %v", p, err)
		}
	}
}
