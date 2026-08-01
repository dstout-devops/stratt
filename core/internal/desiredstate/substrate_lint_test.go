package desiredstate

import (
	"os"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// TestNothingAboveAProviderNamesASubstrate is ADR-0151 follow-up 4's guard, exercised on the
// shapes it exists to catch.
//
// The property ADR-0151 sells is that ONE line — `substrate: kubernetes` in a
// capability-binding — migrates a whole topology, and that nothing above it moves. That is only
// true if nothing above it is separately coupled to a landscape, and a free-form `spec` /
// `values` / `defaults` map is opaque to core (§1.5), so nothing typed could have noticed.
func TestNothingAboveAProviderNamesASubstrate(t *testing.T) {
	providers := Declarations{
		Actuators: []types.Actuator{{Name: "kubecompute"}},
	}

	cases := []struct {
		name  string
		decls Declarations
		want  string
	}{
		{
			name: "an Intent naming a substrate in its spec",
			decls: Declarations{Intents: []types.Intent{{
				Name: "kube-app", Kind: types.IntentCompute,
				Spec: map[string]any{"count": 1, "substrate": "kubernetes"},
			}}},
			want: "spec.substrate",
		},
		{
			name: "an Intent naming a substrate NESTED under params",
			decls: Declarations{Intents: []types.Intent{{
				Name: "kube-app", Kind: types.IntentCompute,
				Spec: map[string]any{"params": map[string]any{"substrate": "aws"}},
			}}},
			want: "spec.params.substrate",
		},
		{
			name: "an Intent naming a declared PROVIDER",
			decls: Declarations{
				Actuators: providers.Actuators,
				Intents: []types.Intent{{
					Name: "kube-app", Kind: types.IntentCompute,
					Spec: map[string]any{"params": map[string]any{"provider": "kubecompute"}},
				}},
			},
			want: "spec.params.provider",
		},
		{
			name: "a Blueprint default",
			decls: Declarations{Blueprints: []types.Blueprint{{
				Name: "web-server", Defaults: map[string]any{"substrate": "vsphere"},
			}}},
			want: "defaults.substrate",
		},
		{
			name: "a Blueprint route's remediationParams",
			decls: Declarations{Blueprints: []types.Blueprint{{
				Name: "web-server",
				Routes: []types.BlueprintRoute{{
					RemediationParams: map[string]any{"substrate": "aws"},
				}},
			}}},
			want: "routes[0].remediationParams.substrate",
		},
		{
			name: "an Assignment value",
			decls: Declarations{Assignments: []types.Assignment{{
				Name: "web-servers-apache", Values: map[string]any{"substrate": "kubernetes"},
			}}},
			want: "values.substrate",
		},
		{
			// A selector reads OBSERVED labels, so this is the one that looks legitimate. It is
			// not: it makes the View's MEMBERSHIP substrate-specific, so the binding line stops
			// migrating it and the Assignment keeps pointing at a set defined by the landscape it
			// used to be on.
			name: "a View selector label",
			decls: Declarations{Views: []Declaration{{
				Name: "web-servers",
				Selector: types.ViewSelector{
					Labels: map[string]string{"substrate": "kubernetes"},
				},
			}}},
			want: "selector.labels.substrate",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkNothingAboveAProviderNamesASubstrate(c.decls)
			if err == nil {
				t.Fatalf("%s must be refused — it re-couples the declaration to a landscape the "+
					"capability-binding is supposed to own (ADR-0151 D1/D2)", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("the error must name the FIELD (§1.8), want %q in: %v", c.want, err)
			}
		})
	}
}

// The lint must not fire on prose, on a capability class, or on a provider coordinate. Each of
// these would be a false positive that costs an estate author real time, and the last two are
// the shapes ADR-0151 actively PRESCRIBES — a lint that refused them would forbid the compliant
// estate.
func TestTheSubstrateLintDoesNotFireOnTheCompliantShapes(t *testing.T) {
	decls := Declarations{
		Actuators: []types.Actuator{{Name: "kubecompute"}, {Name: "awsec2"}},
		Intents: []types.Intent{{
			// `requires` names a capability CLASS; `region` is the provider's own coordinate
			// (ADR-0142 D3); `ami` merely contains the letters of a substrate.
			Name: "aws-billing-fleet", Kind: types.IntentCompute,
			Spec: map[string]any{
				"count":    1,
				"requires": []any{"provisioning"},
				"params": map[string]any{
					"region": "us-east-1",
					"ami":    "ami-0aws-baseline",
				},
			},
		}},
		Blueprints: []types.Blueprint{{
			Name: "web-server",
			Routes: []types.BlueprintRoute{{
				// The class form, which is the whole point of ADR-0135 D3.
				RemediationCapability: "configmgmt",
			}},
		}},
		Views: []Declaration{{
			Name:     "web-servers",
			Selector: types.ViewSelector{Labels: map[string]string{"role": "web"}},
		}},
	}
	if err := checkNothingAboveAProviderNamesASubstrate(decls); err != nil {
		t.Fatalf("the compliant shape must load — a lint that refuses it forbids the estate "+
			"ADR-0151 prescribes: %v", err)
	}
}

// The reference estate must satisfy its own rule. Without this the lint is enforced against
// hypothetical estates only, and the repo's own declarations are exempt by accident.
func TestTheReferenceEstateNamesNoSubstrateAboveAProvider(t *testing.T) {
	if _, err := os.Stat(estateRoot); err != nil {
		t.Skipf("reference estate not found at %s (%v)", estateRoot, err)
	}
	decls, err := ParseDir(estateRoot, nil)
	if err != nil {
		t.Fatalf("reference estate does not parse: %v", err)
	}
	if err := checkNothingAboveAProviderNamesASubstrate(decls); err != nil {
		t.Fatalf("the reference estate violates ADR-0151 follow-up 4: %v", err)
	}
}
