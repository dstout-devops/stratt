package desiredstate

import (
	"os"
	"path/filepath"
	"testing"
)

// demosRoot is the demo library (/demos), relative to this package dir.
const demosRoot = "../../../demos"

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
	dirs, err := filepath.Glob(filepath.Join(demosRoot, "*", "estate"))
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Fatalf("no demo estates found under %s — this guard must not pass by finding nothing", demosRoot)
	}
	for _, dir := range dirs {
		name := filepath.Base(filepath.Dir(dir))
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
