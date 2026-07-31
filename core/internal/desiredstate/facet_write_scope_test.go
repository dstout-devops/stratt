package desiredstate

import (
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// scopeStep builds a one-Step Workflow writing the given namespaces.
func scopeStep(ns ...string) types.Workflow {
	return types.Workflow{
		Name:  "converge",
		Steps: []types.Step{{Name: "apply", FacetWriteScope: ns}},
	}
}

// remediatingBlueprint is the ordinary way a namespace acquires an owner: a route that REMEDIATES
// it. A route that only observes deliberately does not.
func remediatingBlueprint(ns, workflow string) types.Blueprint {
	return types.Blueprint{
		Name: "bp", Version: 1,
		Routes: []types.BlueprintRoute{{
			Observe:             types.FacetExpectation{Namespace: ns},
			RemediationWorkflow: workflow,
		}},
	}
}

func TestFacetWriteScopeNeedsAnOwner(t *testing.T) {
	err := checkFacetWriteScopeOwners(Declarations{Workflows: []types.Workflow{scopeStep("fileset.content")}})
	if err == nil {
		t.Fatal("a Step writing a namespace nothing owns must be refused at LOAD — otherwise the Run " +
			"fails at the far end of a gate an operator has already approved")
	}
	// §1.8: the diagnosis has to name the namespace AND the site, or an operator is left grepping.
	for _, want := range []string{"fileset.content", "converge", "apply", "registration precedes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("diagnosis omits %q: %v", want, err)
		}
	}
}

func TestFacetWriteScopeSatisfiedByARemediatingRoute(t *testing.T) {
	if err := checkFacetWriteScopeOwners(Declarations{
		Blueprints: []types.Blueprint{remediatingBlueprint("fileset.content", "converge")},
		Workflows:  []types.Workflow{scopeStep("fileset.content")},
	}); err != nil {
		t.Fatalf("a Blueprint route that remediates the namespace IS its owner: %v", err)
	}
}

// A route that only OBSERVES does not seize write-ownership — compiler.resolveOwnership claims
// ownership only where remediationWorkflow is set, because a pure observation reads a Facet
// somebody else projects (os.kernel is the standing example). This check must mirror that rather
// than inventing a more generous rule, or it would pass an estate the runtime then refuses.
func TestObserveOnlyRouteIsNotAnOwner(t *testing.T) {
	err := checkFacetWriteScopeOwners(Declarations{
		Blueprints: []types.Blueprint{{
			Name: "bp", Version: 1,
			Routes: []types.BlueprintRoute{{Observe: types.FacetExpectation{Namespace: "os.probe"}}},
		}},
		Workflows: []types.Workflow{scopeStep("os.probe")},
	})
	if err == nil {
		t.Fatal("an observe-only route must not count as an owner — the compiler does not register " +
			"one, so accepting it here would pass an estate the runtime refuses")
	}
}

// THE CASE THAT MOTIVATED THE WHOLE CHECK, and the one a naive version gets wrong.
//
// `ansible-certificate` declares `facetNamespaces: [fileset.content, cert.presented]` and is an
// EE-Job Actuator — it has an image, not an address, so no pluginhost ever verifies it and no grant
// is ever registered. Its facetNamespaces is a write CEILING the governor ANDs against; it is not a
// claim. A check that counted every facetNamespaces would have declared this estate healthy and
// missed the exact defect it exists for.
func TestEEJobActuatorCeilingIsNotOwnership(t *testing.T) {
	decls := Declarations{
		Actuators: []types.Actuator{{
			Name:            "ansible-certificate",
			Image:           "stratt-ee-crypto:dev",
			FacetNamespaces: []string{"fileset.content", "cert.presented"},
		}},
		Workflows: []types.Workflow{scopeStep("fileset.content")},
	}
	if err := checkFacetWriteScopeOwners(decls); err == nil {
		t.Fatal("an EE-Job Actuator's facetNamespaces is a write ceiling, not ownership — counting " +
			"it would pass the very estate that failed live with 'no registered owner'")
	}
	if !strings.Contains(checkFacetWriteScopeOwners(decls).Error(), "CEILING") {
		t.Error("the diagnosis must say that declaring the namespace on the Actuator is not the fix")
	}
}

// A DIALLED provider's facetNamespaces IS ownership: pluginhost registers the grant when it
// verifies the plugin it connects to. `address` versus `image` is the whole discriminator.
func TestDialledProviderFacetNamespacesAreOwnership(t *testing.T) {
	if err := checkFacetWriteScopeOwners(Declarations{
		Actuators: []types.Actuator{{
			Name:            "kubecompute",
			Address:         "stratt-kubecompute:9090",
			FacetNamespaces: []string{"mgmt.address"},
		}},
		Workflows: []types.Workflow{scopeStep("mgmt.address")},
	}); err != nil {
		t.Fatalf("a dialled provider owns its declared facetNamespaces: %v", err)
	}
}

// The namespaces core registers at boot are owned without any declaration claiming them. Read from
// types rather than restated here, so this test cannot pass while the daemon and the check disagree
// about the list — which is the same class of split this whole check is about.
func TestCoreOwnedNamespacesNeedNoDeclaration(t *testing.T) {
	for _, ns := range append(types.TeamOwnedFacetNamespaces(), types.ProjectorOwnedFacetNamespaces()...) {
		if err := checkFacetWriteScopeOwners(Declarations{Workflows: []types.Workflow{scopeStep(ns)}}); err != nil {
			t.Errorf("%s is registered by core at boot and needs no declaration: %v", ns, err)
		}
	}
}

// A Trigger writes back exactly as a Step does (ADR-0054), so it is checked the same way. This is
// not hypothetical: the two ansible collectors were the declarations that made the check fail when
// it first ran, and their namespaces were owned only by the reference estate's Blueprints.
func TestTriggerWriteScopeIsCheckedToo(t *testing.T) {
	err := checkFacetWriteScopeOwners(Declarations{
		Triggers: []types.Trigger{{Name: "access-collector", FacetWriteScope: []string{"access.grants"}}},
	})
	if err == nil {
		t.Fatal("a Trigger's facetWriteScope must be checked — it projects on a cadence, so an " +
			"unowned namespace there fails on a schedule with nobody watching")
	}
	if !strings.Contains(err.Error(), "access-collector") {
		t.Errorf("diagnosis must name the Trigger: %v", err)
	}
}
