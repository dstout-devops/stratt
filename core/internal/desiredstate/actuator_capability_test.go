package desiredstate

import (
	"strings"
	"testing"
)

// A Trigger or Baseline may name a capability CLASS instead of an Actuator (ADR-0140 D4).
// Reconcile is the loop this platform never stops running and it was the least capability-typed
// path in the estate — the provider declared `certissuer`, the Trigger named a provider, and
// nothing connected the two.

// certIssuerEstate writes a floor with one certissuer provider and a Trigger naming the class.
// grant is the provider's facetNamespaces; scope is the Trigger's facetWriteScope.
func certIssuerEstate(t *testing.T, grant, scope string) string {
	t.Helper()
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: certs\nselector: {kinds: [certificate]}\n")
	writeKind(t, root, "actuators", "ci.yaml",
		"name: cert-issuer\naddress: stratt-openbao:9090\npluginIdentity: openbao\ntier: trusted\n"+
			"dryRunnable: true\nprovides: [certissuer]\nfacetNamespaces: ["+grant+"]\n"+
			"identitySchemes: [cert.serial]\n")
	writeKind(t, root, "triggers", "t.yaml",
		"name: cert-reconcile\nkind: schedule\ncron: \"0 */6 * * *\"\nviewName: certs\n"+
			"principal: svc-cert\nactuatorCapability: certissuer\ncredentialRefs: [cert-issuer]\n"+
			"facetWriteScope: ["+scope+"]\n"+
			"params:\n  commonName: web.stratt.test\n  role: stratt-dev\n  ttl: 720h\n  renewBefore: 168h\n  csr: x\n")
	return root
}

func TestActuatorCapabilityTriggerLoads(t *testing.T) {
	root := certIssuerEstate(t, "cert.identity, cert.expiry", "cert.identity, cert.expiry")
	got, err := ParseDir(root, nil)
	if err != nil {
		t.Fatalf("a Trigger naming a class whose sole provider grants the declared scope must load: %v", err)
	}
	tr := got.Triggers[0]
	if tr.ActuatorCapability != "certissuer" {
		t.Fatalf("actuatorCapability must survive the parse, got %q", tr.ActuatorCapability)
	}
	if tr.Actuator != "" {
		t.Fatalf("the class must NOT be resolved to a provider at load — binding is a fire-time fact, "+
			"and baking it in would freeze the binding into the estate: got %q", tr.Actuator)
	}
}

// THE D4 rule, and the reason it is a load-time check rather than a launch-time one. A namespace
// outside the resolved provider's grant is not an error at run time — grant ∩ scope simply drops
// it at the one governor, so the reconcile converges the backend and the graph quietly stops being
// updated. That reports as NOTHING AT ALL, which is why it cannot wait for a bind.
func TestFacetWriteScopeOutsideACandidateGrantIsRefused(t *testing.T) {
	root := certIssuerEstate(t, "cert.identity", "cert.identity, cert.expiry")
	_, err := ParseDir(root, nil)
	if err == nil {
		t.Fatal("a facetWriteScope naming a namespace the candidate provider does not grant must be refused")
	}
	for _, want := range []string{"cert.expiry", "cert-issuer", "silently DROP"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the diagnostic must name the namespace, the provider, and the consequence — a "+
				"dropped write is invisible, so the message is the only warning: missing %q in %v", want, err)
		}
	}
}

// The case D4's trap is actually about: TWO providers, where the scope fits one and exceeds the
// other. Checking only the provider bound today would admit this, and the write-back would vanish
// on the day someone rebinds — which is precisely the moment nobody is watching the reconcile.
func TestFacetWriteScopeIsCheckedAgainstEVERYCandidate(t *testing.T) {
	root := certIssuerEstate(t, "cert.identity, cert.expiry", "cert.identity, cert.expiry")
	// A second, narrower provider of the same class. The Trigger is unchanged and still valid
	// against the first one.
	writeKind(t, root, "actuators", "ci2.yaml",
		"name: stepca\naddress: stratt-stepca:9090\npluginIdentity: stepca\ntier: trusted\n"+
			"dryRunnable: true\nprovides: [certissuer]\nfacetNamespaces: [cert.identity]\n"+
			"identitySchemes: [cert.serial]\n")

	_, err := ParseDir(root, nil)
	if err == nil {
		t.Fatal("a scope that fits one candidate and exceeds another must be refused — checking only " +
			"the bound provider hands a rebind the power to silently narrow write-back")
	}
	if !strings.Contains(err.Error(), "stepca") {
		t.Fatalf("the diagnostic must name the candidate that would drop the write, not the one that works: %v", err)
	}
}

// Naming both gives the declaration two answers to "what converges here", and a rule to pick is
// the implicit precedence §2.4 refuses — the concrete name would silently win.
func TestActuatorAndActuatorCapabilityAreMutuallyExclusive(t *testing.T) {
	root := certIssuerEstate(t, "cert.identity, cert.expiry", "cert.identity")
	writeKind(t, root, "triggers", "t.yaml",
		"name: cert-reconcile\nkind: schedule\ncron: \"0 */6 * * *\"\nviewName: certs\n"+
			"principal: svc-cert\nactuator: cert-issuer\nactuatorCapability: certissuer\n"+
			"credentialRefs: [cert-issuer]\nfacetWriteScope: [cert.identity]\n"+
			"params:\n  commonName: web.stratt.test\n  role: stratt-dev\n  ttl: 720h\n  renewBefore: 168h\n  csr: x\n")
	_, err := ParseDir(root, nil)
	if err == nil {
		t.Fatal("a declaration naming both an actuator and an actuatorCapability must be refused")
	}
	if !strings.Contains(err.Error(), "never both") {
		t.Fatalf("the diagnostic must say they are exclusive: %v", err)
	}
}

// A class no declared ACTUATOR provides can never bind. Refusing at load beats resolving to
// nothing at fire time, six hours later, in a log nobody is reading.
func TestActuatorCapabilityWithNoCandidateIsRefused(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: certs\nselector: {kinds: [certificate]}\n")
	writeKind(t, root, "triggers", "t.yaml",
		"name: cert-reconcile\nkind: schedule\ncron: \"@daily\"\nviewName: certs\n"+
			"principal: svc-cert\nactuatorCapability: nosuchclass\n")
	_, err := ParseDir(root, nil)
	if err == nil {
		t.Fatal("a class no declared Actuator provides must be refused at load")
	}
	if !strings.Contains(err.Error(), "no declared Actuator provides") {
		t.Fatalf("the diagnostic must say what is missing: %v", err)
	}
}

// Params are checked against every candidate's OWN input Contract, because an Actuator-shaped
// class has no class-level Contract the way an Action-shaped one does (ADR-0111/0112) and
// inventing one no shipping Contract demands would violate §1.1. The guarantee is the same: the
// declaration is valid whichever provider binds.
func TestActuatorCapabilityParamsAreCheckedAgainstEveryCandidate(t *testing.T) {
	root := certIssuerEstate(t, "cert.identity, cert.expiry", "cert.identity")
	// A candidate whose input Contract is a different tool's entirely — params valid for
	// cert-issuer cannot satisfy it, and that must surface here rather than on a rebind.
	writeKind(t, root, "actuators", "ci2.yaml",
		"name: helm\naddress: stratt-helm:9090\npluginIdentity: helm\ntier: trusted\n"+
			"dryRunnable: true\nprovides: [certissuer]\nfacetNamespaces: [cert.identity, cert.expiry]\n"+
			"identitySchemes: [cert.serial]\n")
	_, err := ParseDir(root, nil)
	if err == nil {
		t.Fatal("params that satisfy only one candidate must be refused — a class whose providers " +
			"disagree on param shape is not yet an interface")
	}
	if !strings.Contains(err.Error(), "helm") {
		t.Fatalf("the diagnostic must name the candidate that rejects the params: %v", err)
	}
}
