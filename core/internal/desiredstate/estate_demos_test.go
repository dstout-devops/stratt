package desiredstate

import (
	"os"
	"path/filepath"
	"testing"
)

// demosRoot is the CROSS-PLUGIN demo library (/demos), relative to this package dir.
// Single-plugin demos do not live here — see demoEstates.
const demosRoot = "../../../demos"

// pluginsRoot is /plugins, where a single-plugin demo lives beside the plugin it
// demonstrates (ADR-0137 D7).
const pluginsRoot = "../../../plugins"

// demoEstates finds EVERY demo estate, in both homes: the cross-plugin library
// (demos/<name>/estate) and each plugin's own demo (plugins/<name>/demo/estate).
//
// Both, deliberately. ADR-0137 D7 split the demos by whether they span more than one
// plugin, and the guards in this package must not have been quietly narrowed by that
// move — a demo estate that stopped being checked because it changed directory is
// worse than one that was never checked, because the census assertion still passes
// and the coverage looks intact. Every caller asserts a non-empty result for the same
// reason.
func demoEstates(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, pat := range []string{
		filepath.Join(demosRoot, "*", "estate"),
		filepath.Join(pluginsRoot, "*", "demo", "estate"),
	} {
		dirs, err := filepath.Glob(pat)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, dirs...)
	}
	return out
}

// demoLabel names a demo estate for a subtest: the demo's own directory name, which
// is <name> in demos/<name>/estate and in plugins/<plugin>/demo/estate is the plugin.
func demoLabel(dir string) string {
	parent := filepath.Dir(dir)
	if filepath.Base(parent) == "demo" {
		return filepath.Base(filepath.Dir(parent)) + "/demo"
	}
	return filepath.Base(parent)
}

// TestDemoEstatesParse is the standing guard that every demo in the library stays a
// valid, reconcilable desired-state tree — the same ParseDir the daemon runs at boot.
//
// It exists because nothing covered `demos/` at all: `task ci` runs no demo task and no
// test read the tree, so a demo estate could only be found broken by standing up a kind
// cluster and running it. That is a slow, manual gate on files whose entire purpose is to
// be the first thing a newcomer runs — and the estates are delivered as CaC into the
// declarations mount, so a parse error there does not fail the demo cleanly, it leaves a
// floor whose desired state silently never reconciled.
//
// Deliberately shape-agnostic beyond parsing: a demo is free to declare whatever kinds it
// teaches. The one census assertion is that we found demos at all, so this test cannot
// quietly pass by discovering nothing.
func TestDemoEstatesParse(t *testing.T) {
	if _, err := os.Stat(demosRoot); err != nil {
		t.Skipf("demo library not found at %s (%v)", demosRoot, err)
	}
	dirs := demoEstates(t)
	if len(dirs) == 0 {
		t.Fatalf("no demo estates found under %s — this guard must not pass by finding nothing", demosRoot)
	}
	for _, dir := range dirs {
		name := demoLabel(dir)
		t.Run(name, func(t *testing.T) {
			decls, err := ParseDir(dir, nil)
			if err != nil {
				t.Fatalf("demo %q estate does not parse/validate: %v", name, err)
			}
			// A demo estate REPLACES the declarations mount rather than layering onto the
			// reference estate (ADR-0116 D1), so it has to be self-contained. The cheapest
			// true check of that: every Workflow Step naming a View must find it here, and
			// every CredentialRef a Step uses must be declared here. A Step pointing at a
			// View that only exists in /estate would boot a floor that cannot resolve its
			// own targets.
			views := map[string]bool{}
			for _, v := range decls.Views {
				views[v.Name] = true
			}
			refs := map[string]bool{}
			for _, c := range decls.CredentialRefs {
				refs[c.Name] = true
			}
			for _, wf := range decls.Workflows {
				for _, st := range wf.Steps {
					if st.ViewName != "" && !views[st.ViewName] {
						t.Errorf("workflow %q step %q targets View %q, which this demo does not declare",
							wf.Name, st.Name, st.ViewName)
					}
					for _, ref := range st.CredentialRefs {
						if !refs[ref] {
							t.Errorf("workflow %q step %q uses CredentialRef %q, which this demo does not declare",
								wf.Name, st.Name, ref)
						}
					}
				}
			}
		})
	}
}
