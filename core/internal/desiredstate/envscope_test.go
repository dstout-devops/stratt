package desiredstate

import (
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// TestScopeToEnvironment proves the apply-set filter (ADR-0057): a scoped daemon
// keeps untagged + in-env launching kinds and drops out-of-env ones, while
// Views/Workflows (targets reached only through a scoped kind) are never filtered.
// An unscoped daemon (env == "") keeps everything.
func TestScopeToEnvironment(t *testing.T) {
	decls := Declarations{
		Views: []Declaration{{Name: "linux-fleet"}, {Name: "web-hosts"}},
		Assignments: []types.Assignment{
			{Name: "universal"}, // untagged → all envs
			{Name: "dev-only", Environments: []string{"dev"}},
			{Name: "prod-only", Environments: []string{"prod"}},
			{Name: "dev-staging", Environments: []string{"dev", "staging"}},
		},
		Triggers: []types.Trigger{
			{Name: "collector"}, // untagged
			{Name: "cert-reconcile", Environments: []string{"prod"}}, // the noisy prod trigger
		},
		Baselines: []types.Baseline{
			{Name: "cis"}, // untagged
			{Name: "prod-baseline", Environments: []string{"prod"}},
		},
		Workflows: []types.Workflow{{Name: "linux-onboard", Steps: []types.Step{{Name: "s"}}}},
	}

	dev := ScopeToEnvironment(decls, "dev")
	assertNames(t, "dev assignments", names(dev.Assignments, func(a types.Assignment) string { return a.Name }),
		[]string{"universal", "dev-only", "dev-staging"})
	assertNames(t, "dev triggers", names(dev.Triggers, func(x types.Trigger) string { return x.Name }),
		[]string{"collector"}) // cert-reconcile (prod) dropped — the whole point
	assertNames(t, "dev baselines", names(dev.Baselines, func(x types.Baseline) string { return x.Name }),
		[]string{"cis"})
	if len(dev.Views) != 2 || len(dev.Workflows) != 1 {
		t.Errorf("Views/Workflows must never be env-filtered: got %d views, %d workflows", len(dev.Views), len(dev.Workflows))
	}

	// Unscoped daemon keeps everything.
	all := ScopeToEnvironment(decls, "")
	if len(all.Assignments) != 4 || len(all.Triggers) != 2 || len(all.Baselines) != 2 {
		t.Errorf("unscoped daemon must keep all: got %d/%d/%d assignments/triggers/baselines",
			len(all.Assignments), len(all.Triggers), len(all.Baselines))
	}
}

func names[T any](xs []T, f func(T) string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = f(x)
	}
	return out
}

func assertNames(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
	set := map[string]bool{}
	for _, g := range got {
		set[g] = true
	}
	for _, w := range want {
		if !set[w] {
			t.Errorf("%s: missing %q (got %v)", what, w, got)
		}
	}
}
