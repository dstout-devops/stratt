package capability

import (
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func bind(entries ...types.BindingEntry) []types.CapabilityBinding {
	return []types.CapabilityBinding{{Name: "b", Entries: entries}}
}

func builders() []Provider {
	return []Provider{
		{Name: "awsec2", Substrate: types.SubstrateAWS, Workflows: map[string]string{"Compute": "compute-build"}},
		{Name: "kubecompute", Substrate: types.SubstrateKubernetes, Workflows: map[string]string{"Compute": "kubecompute-build"}},
		{Name: "vcenter", Substrate: types.SubstrateVSphere, Workflows: map[string]string{"Compute": "vsphere-vm-build"}},
	}
}

// THE POINT OF ADR-0151 D2: one line selects the builder for every kind that substrate can build,
// so a whole topology migrates by changing that line — and the Intent never names a substrate.
func TestSubstrateSelectsTheBuilder(t *testing.T) {
	for _, tc := range []struct{ substrate, want string }{
		{types.SubstrateKubernetes, "kubecompute"},
		{types.SubstrateAWS, "awsec2"},
		{types.SubstrateVSphere, "vcenter"},
	} {
		// No intentKind on the entry — it covers every kind the substrate builds.
		got := Resolve("provisioning", "Compute", builders(),
			bind(types.BindingEntry{Capability: "provisioning", Substrate: tc.substrate}))
		if got.Status != StatusResolved || got.Provider != tc.want {
			t.Fatalf("substrate %q must resolve to %q, got %+v", tc.substrate, tc.want, got)
		}
	}
}

// The migration is literally one changed value, which is the property the steward asked for.
func TestSubstrateMigrationIsOneLine(t *testing.T) {
	before := Resolve("provisioning", "Compute", builders(),
		bind(types.BindingEntry{Capability: "provisioning", Substrate: types.SubstrateAWS}))
	after := Resolve("provisioning", "Compute", builders(),
		bind(types.BindingEntry{Capability: "provisioning", Substrate: types.SubstrateKubernetes}))
	if before.Provider != "awsec2" || after.Provider != "kubecompute" {
		t.Fatalf("one changed line must move the builder: %q -> %q", before.Provider, after.Provider)
	}
	if before.Workflow == after.Workflow {
		t.Fatal("the bound build Workflow must move with the provider")
	}
}

// A per-kind provider entry WINS over a substrate entry — the declared specificity rule that makes
// D2's tie-break usable. Without it, naming a provider to break a tie would add a candidate rather
// than resolve one.
func TestProviderEntryOverridesSubstrateForOneKind(t *testing.T) {
	provs := append(builders(), Provider{
		Name: "opentofu-network", Substrate: types.SubstrateAWS,
		Workflows: map[string]string{"Subnet": "opentofu-subnet-build"},
	})
	bs := bind(
		types.BindingEntry{Capability: "provisioning", Substrate: types.SubstrateKubernetes},
		types.BindingEntry{Capability: "provisioning", Provider: "opentofu-network", IntentKind: "Subnet"},
	)
	// The override applies to its kind…
	if got := Resolve("provisioning", "Subnet", provs, bs); got.Status != StatusResolved || got.Provider != "opentofu-network" {
		t.Fatalf("a per-kind provider entry must win for its kind, got %+v", got)
	}
	// …and leaves the substrate default in force for every other.
	if got := Resolve("provisioning", "Compute", provs, bs); got.Status != StatusResolved || got.Provider != "kubecompute" {
		t.Fatalf("the substrate default must still hold for other kinds, got %+v", got)
	}
}

// Two substrates in scope is a §2.4 conflict, not a pick: an environment builds on ONE.
func TestTwoSubstratesAreAmbiguous(t *testing.T) {
	got := Resolve("provisioning", "Compute", builders(), bind(
		types.BindingEntry{Capability: "provisioning", Substrate: types.SubstrateAWS},
		types.BindingEntry{Capability: "provisioning", Substrate: types.SubstrateKubernetes},
	))
	if got.Status != StatusAmbiguous {
		t.Fatalf("two substrates must conflict, got %+v", got)
	}
	for _, want := range []string{"aws", "kubernetes"} {
		if !strings.Contains(got.Reason, want) {
			t.Fatalf("the conflict must name both substrates: %s", got.Reason)
		}
	}
}

// §1.8: "no provider builds this kind" and "no provider OF THIS SUBSTRATE builds this kind" are
// different problems with different fixes, and must not read the same.
func TestUnservedSubstrateNamesWhatWasAvailable(t *testing.T) {
	got := Resolve("provisioning", "Compute", builders(),
		bind(types.BindingEntry{Capability: "provisioning", Substrate: types.SubstrateVM}))
	if got.Status != StatusPending {
		t.Fatalf("an unserved substrate must be PENDING, got %+v", got)
	}
	for _, want := range []string{"vm", "aws", "kubernetes", "vsphere"} {
		if !strings.Contains(got.Reason, want) {
			t.Fatalf("the diagnosis must name the substrate asked for AND those available (%q missing): %s", want, got.Reason)
		}
	}
}

// Two providers of ONE substrate for one kind is refused naming both — picking would be the
// implicit precedence §2.4 forbids.
func TestTwoProvidersOfOneSubstrateAreAmbiguous(t *testing.T) {
	provs := []Provider{
		{Name: "kubecompute", Substrate: types.SubstrateKubernetes, Workflows: map[string]string{"Compute": "a"}},
		{Name: "kubeother", Substrate: types.SubstrateKubernetes, Workflows: map[string]string{"Compute": "b"}},
	}
	got := Resolve("provisioning", "Compute", provs,
		bind(types.BindingEntry{Capability: "provisioning", Substrate: types.SubstrateKubernetes}))
	if got.Status != StatusAmbiguous {
		t.Fatalf("two providers of one substrate must be ambiguous, got %+v", got)
	}
	for _, want := range []string{"kubecompute", "kubeother"} {
		if !strings.Contains(got.Reason, want) {
			t.Fatalf("the refusal must name every candidate (%q missing): %s", want, got.Reason)
		}
	}
}

// A provider that declares no substrate is simply never selected by one — every provider shipped
// before ADR-0151 keeps working through its per-kind bindings, unchanged.
func TestUndeclaredSubstrateIsNeverSelected(t *testing.T) {
	provs := []Provider{{Name: "legacy", Workflows: map[string]string{"Compute": "legacy-build"}}}
	if got := Resolve("provisioning", "Compute", provs,
		bind(types.BindingEntry{Capability: "provisioning", Substrate: types.SubstrateAWS})); got.Status != StatusPending {
		t.Fatalf("a substrate-less provider must not be selected by substrate, got %+v", got)
	}
	if got := Resolve("provisioning", "Compute", provs,
		bind(types.BindingEntry{Capability: "provisioning", Provider: "legacy", IntentKind: "Compute"})); got.Status != StatusResolved {
		t.Fatalf("its per-kind binding must still resolve, got %+v", got)
	}
}
