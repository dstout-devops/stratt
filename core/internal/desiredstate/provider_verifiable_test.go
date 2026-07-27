package desiredstate

import (
	"strings"
	"testing"
)

// A capability provider must have something BEHIND its claim (ADR-0138 D5).
//
// These pin the lesson of a defect that shipped: ADR-0135 D2/D3 declared
// `provides: [configmgmt]` on the ansible Actuator, which is EE-Job and therefore has no
// dial address. Verification meant fetching a Manifest over one, so the advertisement could
// never be honoured — three Assignments stopped compiling on every real floor. Every unit
// test passed, because they resolve through a fake resolver. It took booting a cluster.
//
// The first fix demanded an ADDRESS, which was too strong: it made capability routing
// structurally unavailable to subprocess tools, and `configmgmt`'s first provider is a
// subprocess BY CHARTER (§3, the GPLv3 boundary). D5 keeps what that check was really
// enforcing — a claim must be backed, and the backing must be visible in the diff rather
// than discovered as an empty resolution on a live floor (§1.8) — and drops the part that
// excluded the tools the class existed for.

// TestDialLessProviderWithNoMechanismIsRefused is the refusal that survives: a bare class
// claim, backed by nothing, at the moment it is written.
func TestDialLessProviderWithNoMechanismIsRefused(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	writeKind(t, root, "actuators", "a.yaml",
		"name: cm\npluginIdentity: ansible\njobCommand: [stratt-ansible]\nprovides: [configmgmt]\n")

	_, err := ParseDir(root, nil)
	if err == nil {
		t.Fatal("a dial-less Actuator declaring `provides` and no mechanism must be refused — the claim is " +
			"admitted, never resolvable, and reported nowhere")
	}
	// The diagnostic must carry the FIX, not just the rule. "no address" alone would send an
	// author to add an address to a subprocess actuator, which is worse than the mistake.
	for _, want := range []string{"no mechanism", "provisions/remediates/decommissions", "ADR-0138"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("diagnostic is missing %q, so it does not say what to do: %v", want, err)
		}
	}
}

// TestDialLessProviderWithAMechanismIsAdmitted is what D5 unblocked, and it is the shape the
// shipped ansible-platform-baseline Actuator is in. The `remediates` map names a Workflow the
// loader independently requires to exist, so the claim is corroborated against a different part
// of the tree than the one making it — not self-certifying.
func TestDialLessProviderWithAMechanismIsAdmitted(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	writeKind(t, root, "workflows", "w.yaml",
		"name: converge\nsteps:\n  - name: go\n    viewName: hosts\n    actuator: cm\n")
	writeKind(t, root, "actuators", "a.yaml",
		"name: cm\npluginIdentity: ansible\njobCommand: [stratt-ansible]\nprovides: [configmgmt]\n"+
			"remediates:\n  Application: converge\n")

	if _, err := ParseDir(root, nil); err != nil {
		t.Fatalf("a dial-less provider that declares a mechanism must be admitted — configmgmt's first "+
			"provider is a subprocess by charter, and a capability system that excludes it cannot express "+
			"the class: %v", err)
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
