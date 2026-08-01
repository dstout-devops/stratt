package desiredstate

import (
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// The whole point of ADR-0142 D2 is that an unknown environment stops being a silent
// no-op. These assert it fires, and — just as important — that it stays off for an estate
// that has not opted in, because over-firing would break every self-contained demo estate.
func TestValidateEnvironmentRefs(t *testing.T) {
	declared := []types.Environment{{Name: "dev"}, {Name: "prod"}}

	t.Run("a typo'd environment is refused", func(t *testing.T) {
		err := validateEnvironmentRefs(Declarations{
			Environments: declared,
			Assignments:  []types.Assignment{{Name: "web-server", Environments: []string{"dveelop"}}},
		})
		if err == nil {
			t.Fatal("an undeclared environment must be refused — otherwise it filters the declaration " +
				"out of every environment, permanently and silently")
		}
		// The message must name the offender AND the legal set, or the author cannot act (§1.8).
		for _, want := range []string{"web-server", "dveelop", "dev prod"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("message must contain %q, got: %v", want, err)
			}
		}
	})

	t.Run("every env-carrying kind is checked", func(t *testing.T) {
		cases := []struct {
			name string
			d    Declarations
		}{
			{"assignment", Declarations{Assignments: []types.Assignment{{Name: "a", Environments: []string{"nope"}}}}},
			{"trigger", Declarations{Triggers: []types.Trigger{{Name: "t", Environments: []string{"nope"}}}}},
			{"baseline", Declarations{Baselines: []types.Baseline{{Name: "b", Environments: []string{"nope"}}}}},
			{"connector", Declarations{Connectors: []types.Connector{{Name: "c", Environments: []string{"nope"}}}}},
			{"capability-binding", Declarations{CapabilityBindings: []types.CapabilityBinding{{Name: "cb", Environments: []string{"nope"}}}}},
		}
		for _, tc := range cases {
			tc.d.Environments = declared
			if err := validateEnvironmentRefs(tc.d); err == nil {
				t.Errorf("%s: an undeclared environment must be refused on this kind too", tc.name)
			}
		}
	})

	t.Run("declared environments pass", func(t *testing.T) {
		if err := validateEnvironmentRefs(Declarations{
			Environments: declared,
			Assignments:  []types.Assignment{{Name: "a", Environments: []string{"dev", "prod"}}},
			Triggers:     []types.Trigger{{Name: "t", Environments: []string{"prod"}}},
		}); err != nil {
			t.Fatalf("declared scopes must pass: %v", err)
		}
	})

	t.Run("an unscoped declaration is always fine", func(t *testing.T) {
		if err := validateEnvironmentRefs(Declarations{
			Environments: declared,
			Assignments:  []types.Assignment{{Name: "a"}},
		}); err != nil {
			t.Fatalf("empty environments means UNSCOPED (ADR-0057 D2), not unknown: %v", err)
		}
	})

	// The compatibility rule, and the reason it exists. A demo estate replaces the whole
	// declarations mount and must be self-contained (ADR-0116 D1); enforcing a closed set
	// against an estate that declares none would break every one of them for no safety gain.
	t.Run("an estate declaring no environments is not policed", func(t *testing.T) {
		if err := validateEnvironmentRefs(Declarations{
			Assignments: []types.Assignment{{Name: "a", Environments: []string{"anything"}}},
		}); err != nil {
			t.Fatalf("declaring one environment is the opt-in; an estate with none has no closed set: %v", err)
		}
	})
}

func TestValidateEnvironment(t *testing.T) {
	if err := ValidateEnvironment(types.Environment{}); err == nil {
		t.Error("a nameless environment is nothing a filter can match")
	}
	// Whitespace would reintroduce the exact silent-no-op one level down: `[ dev ]` and
	// `dev` would be two different scopes, and the difference is invisible in review.
	for _, bad := range []string{" dev", "dev ", "two words", "\tdev"} {
		if err := ValidateEnvironment(types.Environment{Name: bad}); err == nil {
			t.Errorf("environment name %q must be refused — invisible differences silently match nothing", bad)
		}
	}
	if err := ValidateEnvironment(types.Environment{Name: "vsphere-dc"}); err != nil {
		t.Errorf("a normal name must pass: %v", err)
	}
}
