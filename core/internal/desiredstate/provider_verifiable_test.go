package desiredstate

import (
	"strings"
	"testing"
)

// A capability provider must be reachable for VERIFICATION (ADR-0138 D5).
//
// These pin the lesson of a defect that shipped: ADR-0135 D2/D3 declared
// `provides: [configmgmt]` on the ansible Actuator, which is EE-Job and therefore has
// no dial address. Capability resolution counts only verified providers, verification
// fetches a Manifest over that address, so the advertisement could never be honoured —
// three Assignments stopped compiling on every real floor. Every unit test passed,
// because they resolve through a fake resolver. It took booting a cluster to find.

// TestEEJobActuatorCannotProvideACapability is the refusal, at the moment the
// declaration is written rather than the moment a floor tries to use it.
func TestEEJobActuatorCannotProvideACapability(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	writeKind(t, root, "actuators", "a.yaml",
		"name: cm\npluginIdentity: ansible\njobCommand: [stratt-ansible]\nprovides: [configmgmt]\n")

	_, err := ParseDir(root, nil)
	if err == nil {
		t.Fatal("an EE-Job Actuator declaring `provides` must be refused — it can never be verified, so the " +
			"advertisement is admitted, never counted, and reported nowhere")
	}
	// The diagnostic has to carry the CAUSE, not just the rule: an author who reads
	// "no address" without "verification fetches a Manifest over it" will add an
	// address to a subprocess actuator, which is worse than the original mistake.
	for _, want := range []string{"no address", "Manifest", "ADR-0138"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("diagnostic is missing %q, so it does not explain WHY: %v", want, err)
		}
	}
}

// TestDialAddressedActuatorMayProvide is the necessary counterweight. Every provider
// this estate ships is gRPC-addressed — awsec2, vcenter, openbao (keycustodian +
// certissuer), netbox, crossplane, opentofu, awss3 — and the check must leave all of
// them alone. A gate that fired on the working case would be switched off.
func TestDialAddressedActuatorMayProvide(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	writeKind(t, root, "actuators", "a.yaml",
		"name: prov\npluginIdentity: awsec2\naddress: stratt-awsec2:9090\nprovides: [provisioning]\n")

	if _, err := ParseDir(root, nil); err != nil {
		t.Fatalf("a dial-addressed provider must be admitted: %v", err)
	}
}

// TestEEJobActuatorWithoutProvidesIsFine: the check is about being a PROVIDER, not
// about being EE-Job. The ansible Actuators this repo ships are all EE-Job and all
// legal — they simply advertise no capability.
func TestEEJobActuatorWithoutProvidesIsFine(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	writeKind(t, root, "actuators", "a.yaml",
		"name: cm\npluginIdentity: ansible\njobCommand: [stratt-ansible]\nfacetNamespaces: [os.kernel]\n"+
			"identitySchemes: [host.name]\n")

	if _, err := ParseDir(root, nil); err != nil {
		t.Fatalf("an EE-Job Actuator that advertises no capability must stay legal: %v", err)
	}
}

// TestRequiresIsNotGatedByAddress is the half of ADR-0138 D1 that must NOT be
// restricted. Depending on a capability is the intended design — ansible cannot
// provision the hosts it converges — and a subprocess Actuator is exactly the kind of
// tool that needs one. Only PROVIDING requires a dial address; REQUIRING never does.
func TestRequiresIsNotGatedByAddress(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	writeKind(t, root, "actuators", "a.yaml",
		"name: cm\npluginIdentity: ansible\njobCommand: [stratt-ansible]\nrequires: [provisioning]\n")

	if _, err := ParseDir(root, nil); err != nil {
		t.Fatalf("an EE-Job Actuator must be free to REQUIRE a capability it cannot provide (ADR-0138 D1): %v", err)
	}
}
