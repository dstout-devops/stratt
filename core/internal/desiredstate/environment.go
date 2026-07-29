package desiredstate

import (
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/dstout-devops/stratt/types"
)

// Environment declarations (ADR-0142): the referent for the `environments` membership
// filter ADR-0057 introduced. See types.Environment for why this is not a Named Kind and
// why it is deliberately thin.

type environmentFile struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

func parseEnvironmentFile(path string, raw []byte) (string, types.Environment, error) {
	var f environmentFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true) // a typo in a declaration must fail, not silently vanish
	if err := dec.Decode(&f); err != nil {
		return "", types.Environment{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	e := types.Environment{Name: f.Name, Description: f.Description}
	if err := ValidateEnvironment(e); err != nil {
		return "", types.Environment{}, fmt.Errorf("desiredstate: %s: %w", path, err)
	}
	return e.Name, e, nil
}

// ValidateEnvironment enforces the minimum an environment must be: a name that a filter
// can match. Whitespace is refused because `environments: [ dev ]` and `dev` must not be
// two different environments — a scope token that differs only by invisible characters is
// the silent-no-op this ADR exists to close, reintroduced one level down.
func ValidateEnvironment(e types.Environment) error {
	if e.Name == "" {
		return fmt.Errorf("environment: name is required")
	}
	if strings.TrimSpace(e.Name) != e.Name || strings.ContainsAny(e.Name, " \t") {
		return fmt.Errorf("environment %q: name must not contain surrounding or embedded whitespace — "+
			"a scope token differing only by invisible characters silently matches nothing (§1.8)", e.Name)
	}
	return nil
}

// validateEnvironmentRefs checks every `environments:` reference in the estate against the
// declared set (ADR-0142 D2).
//
// The failure this closes: ScopeToEnvironment filters on a bare string membership test
// against no registry, so an unknown name is not an error — it is a declaration that is
// filtered out of EVERY environment, forever, silently. The estate looks right in Git and
// does nothing. This is the same rule the package already applies to an uncontracted
// Action, a Step naming an undeclared Actuator, and a playbook missing from its content
// root: refuse at the diff, not at the moment someone wonders why nothing happened.
//
// COMPATIBILITY, stated rather than implicit (ADR-0142 D2): the check applies only when
// the estate declares at least one Environment. An estate declaring none has no closed set
// to check against, and enforcing would break every self-contained demo estate (ADR-0116
// D1) for no safety gain. Declaring one is the opt-in — the same "locality is not
// authority, the estate opts in" posture as estate/plugins.yaml (ADR-0137 D3).
func validateEnvironmentRefs(d Declarations) error {
	if len(d.Environments) == 0 {
		return nil
	}
	declared := make(map[string]bool, len(d.Environments))
	for _, e := range d.Environments {
		declared[e.Name] = true
	}
	known := make([]string, 0, len(declared))
	for n := range declared {
		known = append(known, n)
	}
	sort.Strings(known)

	// Every declaration kind that carries the filter. Kept as an explicit list rather than
	// reflection so that adding a kind with `environments` and forgetting it here is a
	// visible omission in review, not an invisible one.
	refs := []struct {
		kind string
		name string
		envs []string
	}{}
	for _, a := range d.Assignments {
		refs = append(refs, struct {
			kind string
			name string
			envs []string
		}{"assignment", a.Name, a.Environments})
	}
	for _, t := range d.Triggers {
		refs = append(refs, struct {
			kind string
			name string
			envs []string
		}{"trigger", t.Name, t.Environments})
	}
	for _, b := range d.Baselines {
		refs = append(refs, struct {
			kind string
			name string
			envs []string
		}{"baseline", b.Name, b.Environments})
	}
	for _, c := range d.Connectors {
		refs = append(refs, struct {
			kind string
			name string
			envs []string
		}{"connector", c.Name, c.Environments})
	}
	// Actuator was MISSING from this list, and it is the one kind where the omission bites
	// hardest: assembleProvisioningProviders filters build providers by
	// `types.InScope(a.ScopedEnvironments(), env)` (provisioning_resolve.go), so an Actuator
	// with a typo'd environment provides nothing, in every environment, and the only symptom
	// is an Intent whose build Finding resolves to no provider — reported as "no verified
	// provider", which sends the reader to the registry rather than to the typo.
	//
	// The comment above says an explicit list makes a forgotten kind "a visible omission in
	// review". It was not visible; it was missed in the ADR that wrote the sentence. The list
	// stays explicit, but a test now derives the expected set by reflection over Declarations,
	// so the next kind carrying `environments` cannot be forgotten quietly.
	for _, a := range d.Actuators {
		refs = append(refs, struct {
			kind string
			name string
			envs []string
		}{"actuator", a.Name, a.Environments})
	}
	for _, b := range d.CapabilityBindings {
		refs = append(refs, struct {
			kind string
			name string
			envs []string
		}{"capability-binding", b.Name, b.Environments})
	}

	for _, r := range refs {
		for _, e := range r.envs {
			if !declared[e] {
				return fmt.Errorf(
					"%s %q: environments references %q, which no environments/ declaration defines "+
						"(declared: %s). An unknown environment is not a no-op — it filters this "+
						"declaration out of EVERY environment, silently and permanently (§1.8, ADR-0142 D2)",
					r.kind, r.name, e, strings.Join(known, " "))
			}
		}
	}
	return nil
}
