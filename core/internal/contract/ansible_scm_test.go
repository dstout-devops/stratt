package contract

import "testing"

// TestAnsibleV3IsResolved locks that the highest-versioned sibling wins the
// actuators/ansible.input lookup (path-sorted load, ADR-0025) — so the scm
// content-ref schema is the one Steps validate against.
func TestAnsibleV3IsResolved(t *testing.T) {
	c, ok, err := Get("actuators/ansible.input")
	if err != nil || !ok {
		t.Fatalf("ansible.input contract: ok=%v err=%v", ok, err)
	}
	if c.Version != 3 {
		t.Fatalf("resolved ansible.input version = %d, want 3 (the scm sibling)", c.Version)
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
}
