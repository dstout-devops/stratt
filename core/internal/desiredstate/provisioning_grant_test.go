package desiredstate

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// TestProvisioningActuatorsGrantTheirCorrelationLabel refuses a `provisioning` provider
// whose grant does not admit the correlation label its own build project-back carries.
//
// The provisioning loop closes on ONE label. A build projects the Entity it made with
// `stratt.intent/instance` (Compute fan-out) or `stratt.intent/singleton` (the named
// singletons), and the next reconcile reads exactly that key back —
// ProvisionedInstances / ProvisionedSingletons select on it and nothing else — to decide
// the desired unit is now built. Without it the Finding never resolves, and the
// reconcile goes on offering a build for a machine that already exists.
//
// And the label is DROPPED, not refused: Host.toUpsert checks allowsLabel per key and
// skips the ones outside the grant (a Rejection on the side channel), so the projection
// still lands, the Entity still appears, and the Run still goes green. Every signal a
// demo or an operator looks at says success. That is precisely why this is worth a
// static check: the only visible symptom is a build offered a second time, days later,
// with nothing pointing at the grant.
//
// FILESYSTEM, not a parse, for the reason its sibling guards are: the declarations under
// test live in trees a running daemon loads selectively, and a repo-wide answer has to
// come from the repo.
func TestProvisioningActuatorsGrantTheirCorrelationLabel(t *testing.T) {
	repo := filepath.Join("..", "..", "..")

	var dirs []string
	for _, pat := range []string{
		filepath.Join(repo, "estate", "actuators"),
		filepath.Join(repo, "plugins", "*", "estate", "actuators"),
		filepath.Join(repo, "plugins", "*", "demo", "estate", "actuators"),
		filepath.Join(repo, "demos", "*", "estate", "actuators"),
	} {
		m, err := filepath.Glob(pat)
		if err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, m...)
	}

	checked := 0
	for _, dir := range dirs {
		for _, f := range yamlsIn(t, dir) {
			var act struct {
				Name       string            `yaml:"name"`
				LabelKeys  []string          `yaml:"labelKeys"`
				Provisions map[string]string `yaml:"provisions"`
			}
			decodeYAML(t, f, &act)
			if len(act.Provisions) == 0 {
				continue
			}
			granted := map[string]bool{}
			for _, k := range act.LabelKeys {
				granted[k] = true
			}
			kinds := make([]string, 0, len(act.Provisions))
			for k := range act.Provisions {
				kinds = append(kinds, k)
			}
			sort.Strings(kinds)
			for _, kind := range kinds {
				checked++
				want := correlationLabelFor(kind)
				if !granted[want] {
					t.Errorf("%s: Actuator %q advertises provisions[%s] but its labelKeys %v do not include %q — "+
						"the build's project-back carries that label and the reconcile reads ONLY it to see the unit "+
						"as built, so the governor drops it, the Run goes green, and the Finding is offered again forever",
						f, act.Name, kind, act.LabelKeys, want)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no Actuator advertises `provisions` — the estate layout changed and this guard is checking nothing")
	}
	t.Logf("checked %d provisions entries", checked)
}

// correlationLabelFor maps an Intent kind to the label its build project-back must
// carry. DERIVED from types.SingletonIntentKinds rather than listed, so a kind added to
// that family inherits the rule instead of needing someone to remember it.
func correlationLabelFor(shortKind string) string {
	if types.SingletonIntentKinds["Intent/"+strings.TrimPrefix(shortKind, "Intent/")] {
		return "stratt.intent/singleton"
	}
	return "stratt.intent/instance"
}
