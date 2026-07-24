package desiredstate

import (
	"testing"

	"github.com/dstout-devops/stratt/core/internal/capability"
	"github.com/dstout-devops/stratt/types"
)

// TestAssembleProvisioningProviders_EnvScoped proves ADR-0113 D2: provider selection is filtered to
// the daemon's active environment (membership, not precedence). vcenter (scoped to vsphere-dc) and
// awsec2 (unscoped ⇒ every environment) both provision Compute; which providers a resolve SEES
// depends only on the environment, and fail-closed within an environment is preserved downstream.
func TestAssembleProvisioningProviders_EnvScoped(t *testing.T) {
	verified := map[string]bool{"actuator/awsec2": true, "actuator/vcenter": true}
	acts := []types.Actuator{
		{Name: "awsec2", Provides: []string{types.CapProvisioning}, Provisions: map[string]string{"Compute": "compute-build"}},
		{Name: "vcenter", Provides: []string{types.CapProvisioning}, Provisions: map[string]string{"Compute": "vsphere-vm-build"}, Environments: []string{"vsphere-dc"}},
	}

	names := func(ps []capability.Provider) map[string]bool {
		m := map[string]bool{}
		for _, p := range ps {
			m[p.Name] = true
		}
		return m
	}

	// vsphere-dc: both awsec2 (unscoped) and vcenter (scoped here) are in scope → 2 Compute builders
	// (ambiguity a binding must resolve — asserted separately in capability.Resolve).
	got := names(assembleProvisioningProviders(verified, acts, nil, "vsphere-dc"))
	if !got["awsec2"] || !got["vcenter"] {
		t.Errorf("vsphere-dc: want both awsec2+vcenter in scope, got %v", got)
	}

	// aws: vcenter is scoped OUT; awsec2 (unscoped) remains → sole Compute builder, auto-binds.
	got = names(assembleProvisioningProviders(verified, acts, nil, "aws"))
	if got["vcenter"] {
		t.Errorf("aws: vcenter (scoped vsphere-dc) must be out of scope, got %v", got)
	}
	if !got["awsec2"] {
		t.Errorf("aws: awsec2 (unscoped) must be in scope, got %v", got)
	}

	// unscoped daemon (env==""): sees everything (InScope short-circuits true).
	got = names(assembleProvisioningProviders(verified, acts, nil, ""))
	if !got["awsec2"] || !got["vcenter"] {
		t.Errorf("unscoped: want everything, got %v", got)
	}
}

// TestAssembleProvisioningProviders_FailClosed: an unverified provider, or one with no provisions
// map, is excluded regardless of environment (ADR-0104 D1).
func TestAssembleProvisioningProviders_FailClosed(t *testing.T) {
	acts := []types.Actuator{
		{Name: "phantom", Provides: []string{types.CapProvisioning}, Provisions: map[string]string{"Compute": "x"}}, // not in verified
		{Name: "nobuild", Provides: []string{types.CapProvisioning}},                                                // verified but no provisions
	}
	verified := map[string]bool{"actuator/nobuild": true}
	got := assembleProvisioningProviders(verified, acts, nil, "")
	if len(got) != 0 {
		t.Errorf("want no providers (phantom unverified, nobuild has no provisions), got %v", got)
	}
}

// TestInScopeBindings proves capability-bindings are env-filtered so an out-of-environment binding
// cannot select a provider in another environment (ADR-0113 D2, §2.4 no cross-env leak).
func TestInScopeBindings(t *testing.T) {
	all := []types.CapabilityBinding{
		{Name: "vsphere", Environments: []string{"vsphere-dc"}, Entries: []types.BindingEntry{{Capability: "provisioning", Provider: "vcenter", IntentKind: "Compute"}}},
		{Name: "global", Entries: []types.BindingEntry{{Capability: "provisioning", Provider: "opentofu-network", IntentKind: "Subnet"}}},
	}
	// aws env: the vsphere-scoped binding is filtered out; the unscoped global one stays.
	got := inScopeBindings(all, "aws")
	if len(got) != 1 || got[0].Name != "global" {
		t.Errorf("aws: want only the global binding, got %v", got)
	}
	// vsphere-dc: both in scope.
	if got := inScopeBindings(all, "vsphere-dc"); len(got) != 2 {
		t.Errorf("vsphere-dc: want both bindings, got %d", len(got))
	}
}
