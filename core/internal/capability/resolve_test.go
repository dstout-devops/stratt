package capability

import (
	"sync"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

const prov = "provisioning"

// Provisions values are the providers' per-kind build WORKFLOW names (ADR-0110 D3).
func awsec2() Provider {
	return Provider{Name: "awsec2", Provisions: map[string]string{"Compute": "compute-build", "Subnet": "awsec2-subnet-build"}}
}
func crossplane() Provider {
	return Provider{Name: "crossplane", Provisions: map[string]string{"Subnet": "subnet-build", "Vlan": "vlan-build"}}
}
func opentofuNetwork() Provider {
	return Provider{Name: "opentofu-network", Provisions: map[string]string{"Subnet": "opentofu-subnet-build"}}
}

func binding(name string, entries ...types.BindingEntry) types.CapabilityBinding {
	return types.CapabilityBinding{Name: name, Entries: entries}
}
func entry(provider, kind string) types.BindingEntry {
	return types.BindingEntry{Capability: prov, Provider: provider, IntentKind: kind}
}

func TestResolve(t *testing.T) {
	cases := []struct {
		name         string
		kind         string
		providers    []Provider
		bindings     []types.CapabilityBinding
		wantStatus   Status
		wantProv     string
		wantWorkflow string
	}{
		{
			name:       "auto-bind sole provider (Compute → awsec2)",
			kind:       "Compute",
			providers:  []Provider{awsec2()},
			wantStatus: StatusResolved, wantProv: "awsec2", wantWorkflow: "compute-build",
		},
		{
			name:       "auto-bind sole VLAN builder (Vlan → crossplane; awsec2 has no VLAN action)",
			kind:       "Vlan",
			providers:  []Provider{awsec2(), crossplane()},
			wantStatus: StatusResolved, wantProv: "crossplane", wantWorkflow: "vlan-build",
		},
		{
			name:       "explicit binding disambiguates Subnet (awsec2 vs crossplane) → awsec2",
			kind:       "Subnet",
			providers:  []Provider{awsec2(), crossplane()},
			bindings:   []types.CapabilityBinding{binding("dev", entry("awsec2", "Subnet"))},
			wantStatus: StatusResolved, wantProv: "awsec2", wantWorkflow: "awsec2-subnet-build",
		},
		{
			// ADR-0112 B1: the live estate case — Subnet has crossplane + opentofu-network; the
			// provisioning-subnet binding demotes crossplane by selecting opentofu-network.
			name:       "B1 demotion: Subnet crossplane vs opentofu-network, binding → opentofu",
			kind:       "Subnet",
			providers:  []Provider{crossplane(), opentofuNetwork()},
			bindings:   []types.CapabilityBinding{binding("provisioning-subnet", entry("opentofu-network", "Subnet"))},
			wantStatus: StatusResolved, wantProv: "opentofu-network", wantWorkflow: "opentofu-subnet-build",
		},
		{
			name:       "≥2 builders, no binding → ambiguous (§2.4)",
			kind:       "Subnet",
			providers:  []Provider{awsec2(), crossplane()},
			wantStatus: StatusAmbiguous,
		},
		{
			name:       "no provider at all → pending (axis a)",
			kind:       "Compute",
			providers:  nil,
			wantStatus: StatusPending,
		},
		{
			name:       "provider(s) exist but none builds this kind → pending (axis b)",
			kind:       "Dmz",
			providers:  []Provider{awsec2(), crossplane()},
			wantStatus: StatusPending,
		},
		{
			name:       "binding names a provider that doesn't build the kind → pending, not fallback",
			kind:       "Vlan",
			providers:  []Provider{awsec2(), crossplane()},
			bindings:   []types.CapabilityBinding{binding("dev", entry("awsec2", "Vlan"))}, // awsec2 has no Vlan action
			wantStatus: StatusPending,
		},
		{
			name:       "binding names an unverified/absent provider → pending",
			kind:       "Compute",
			providers:  []Provider{awsec2()},
			bindings:   []types.CapabilityBinding{binding("dev", entry("gce", "Compute"))}, // gce not in verified set
			wantStatus: StatusPending,
		},
		{
			name:      "conflicting bindings select two providers → ambiguous (§2.4)",
			kind:      "Subnet",
			providers: []Provider{awsec2(), crossplane()},
			bindings: []types.CapabilityBinding{
				binding("a", entry("awsec2", "Subnet")),
				binding("b", entry("crossplane", "Subnet")),
			},
			wantStatus: StatusAmbiguous,
		},
		{
			name:       "duplicate binding entries for the SAME provider are not a conflict → resolved",
			kind:       "Subnet",
			providers:  []Provider{awsec2(), crossplane()},
			bindings:   []types.CapabilityBinding{binding("a", entry("awsec2", "Subnet")), binding("b", entry("awsec2", "Subnet"))},
			wantStatus: StatusResolved, wantProv: "awsec2", wantWorkflow: "awsec2-subnet-build",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Resolve(prov, tc.kind, tc.providers, tc.bindings)
			if got.Status != tc.wantStatus {
				t.Fatalf("status = %v, want %v (reason: %q)", got.Status, tc.wantStatus, got.Reason)
			}
			if tc.wantStatus == StatusResolved {
				if got.Provider != tc.wantProv || got.Workflow != tc.wantWorkflow {
					t.Fatalf("resolved to %q/%q, want %q/%q", got.Provider, got.Workflow, tc.wantProv, tc.wantWorkflow)
				}
			}
			if tc.wantStatus != StatusResolved && got.Reason == "" {
				t.Fatalf("a non-resolved outcome must carry an observable reason (§1.8)")
			}
		})
	}
}

// Resolve must not mutate its inputs and must be safe under concurrent calls (go test -race).
func TestResolveConcurrentReadOnly(t *testing.T) {
	providers := []Provider{awsec2(), crossplane()}
	bindings := []types.CapabilityBinding{binding("dev", entry("awsec2", "Subnet"))}
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r := Resolve(prov, "Subnet", providers, bindings); r.Status != StatusResolved {
				t.Errorf("concurrent resolve regressed: %v", r.Status)
			}
			_ = Resolve(prov, "Vlan", providers, bindings)
			_ = Resolve(prov, "Dmz", providers, bindings)
		}()
	}
	wg.Wait()
	// Inputs untouched.
	if _, ok := providers[0].Provisions["Vlan"]; ok {
		t.Fatal("Resolve mutated a provider's Provisions map")
	}
}
