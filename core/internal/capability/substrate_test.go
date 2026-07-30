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

// THE RULE CHARTER-GUARDIAN SUBSTITUTED FOR THE ONE I FIRST WROTE (2026-07-30). The two selector
// forms COMBINE only where the substrate entry is UNDERDETERMINED on a kind — it offers more than
// one builder and the provider entry names one OF that substrate. They never contest.
//
// My original rule made a per-kind provider entry WIN, which is a specificity ranking, which is
// exactly the anti-GPO precedence §2.4 refuses — and it reproduced the defect ADR-0151 exists to
// eliminate, in shipped config: dev resolved Compute to a kubernetes provider and Subnet to an aws
// one, both green, no diagnosis. See TestMixedSubstrateIsRefusedNotRanked below, which is that
// estate.
func TestProviderEntryCompletesAnUnderdeterminedSubstrate(t *testing.T) {
	// TWO kubernetes builders for one kind — the substrate genuinely leaves the choice open.
	provs := []Provider{
		{Name: "kubecompute", Substrate: types.SubstrateKubernetes, Workflows: map[string]string{"Compute": "a"}},
		{Name: "kubeother", Substrate: types.SubstrateKubernetes, Workflows: map[string]string{"Compute": "b"}},
	}
	// Substrate alone cannot decide…
	if got := Resolve("provisioning", "Compute", provs,
		bind(types.BindingEntry{Capability: "provisioning", Substrate: types.SubstrateKubernetes})); got.Status != StatusAmbiguous {
		t.Fatalf("an underdetermined substrate must refuse, got %+v", got)
	}
	// …and a provider entry naming one OF that substrate closes it. This is the tie-break D2
	// promises, and it survives the ruling because the forms are not answering the same question:
	// the substrate said "kubernetes", the provider entry said "which kubernetes one".
	got := Resolve("provisioning", "Compute", provs, bind(
		types.BindingEntry{Capability: "provisioning", Substrate: types.SubstrateKubernetes},
		types.BindingEntry{Capability: "provisioning", Provider: "kubeother", IntentKind: "Compute"},
	))
	if got.Status != StatusResolved || got.Provider != "kubeother" {
		t.Fatalf("a provider entry must COMPLETE an underdetermined substrate, got %+v", got)
	}
}

// A provider entry may not override a substrate that has ALREADY decided — two bindings answering
// one question is a contest, not a refinement.
func TestProviderEntryCannotOverrideADecidedSubstrate(t *testing.T) {
	got := Resolve("provisioning", "Compute", builders(), bind(
		types.BindingEntry{Capability: "provisioning", Substrate: types.SubstrateKubernetes},
		types.BindingEntry{Capability: "provisioning", Provider: "awsec2", IntentKind: "Compute"},
	))
	if got.Status != StatusAmbiguous {
		t.Fatalf("an override of a decided substrate must be refused, got %+v", got)
	}
}

// THE REGRESSION, and it is the shipped dev estate verbatim: a substrate entry claiming every kind
// beside a provider entry from ANOTHER substrate. Under the rejected rule both resolved green and
// the topology was silently half-Kubernetes, half-AWS.
func TestMixedSubstrateIsRefusedNotRanked(t *testing.T) {
	provs := []Provider{
		{Name: "kubecompute", Substrate: types.SubstrateKubernetes, Workflows: map[string]string{"Compute": "kubecompute-build"}},
		{Name: "opentofu-network", Substrate: types.SubstrateAWS, Workflows: map[string]string{"Subnet": "opentofu-subnet-build"}},
	}
	bs := bind(
		types.BindingEntry{Capability: "provisioning", Substrate: types.SubstrateKubernetes},
		types.BindingEntry{Capability: "provisioning", Provider: "opentofu-network", IntentKind: "Subnet"},
	)
	got := Resolve("provisioning", "Subnet", provs, bs)
	if got.Status != StatusAmbiguous {
		t.Fatalf("a provider of ANOTHER substrate must be refused, not ranked — got %+v", got)
	}
	for _, want := range []string{"kubernetes", "opentofu-network"} {
		if !strings.Contains(got.Reason, want) {
			t.Fatalf("the refusal must name the substrate and the contradicting provider (%q missing): %s", want, got.Reason)
		}
	}
	// A substrate that claims a kind no provider of it can build is still REFUSED, never quietly
	// filled by another substrate's provider — that is the hole the ruling closed.
	if got := Resolve("provisioning", "Compute", provs, bs); got.Status != StatusResolved {
		t.Fatalf("the kind the substrate CAN build must still resolve, got %+v", got)
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
		bind(types.BindingEntry{Capability: "provisioning", Substrate: "openstack"}))
	if got.Status != StatusPending {
		t.Fatalf("an unserved substrate must be PENDING, got %+v", got)
	}
	for _, want := range []string{"openstack", "aws", "kubernetes", "vsphere"} {
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
