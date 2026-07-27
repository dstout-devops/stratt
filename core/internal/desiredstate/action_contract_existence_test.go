package desiredstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestEveryStepActionIsContracted refuses a Workflow Step that names an Action no
// Contract document defines. "An uncontracted operation must not exist" (§2.2,
// ADR-0031) — checked here, at the diff, rather than only when an operator approves
// the build and the Invoke fails.
//
// WHY THIS IS A REPO TEST AND NOT A LOAD-TIME CHECK. It was briefly the latter, in
// validateActionParamsContract, and that was the wrong layer: a running daemon's
// contract set is INCOMPLETE when it parses declarations. A plugin's self contracts
// are read from its own tree only for the owning or an admitted plugin (ADR-0138
// D3/D4), and an environment-wired plugin — the boot-wired path every plugin demo
// uses — contributes its contracts later still, from its Manifest at enable. So a
// daemon seeing "no contract" is usually seeing "not yet", and enforcing it there
// made strattd refuse to boot against a valid estate.
//
// In the repo there is no "not yet": every tree is on disk. So the same rule is
// exact here and merely optimistic there.
//
// Deliberately a FILESYSTEM check rather than a parse: the package-global contract
// registry accumulates across trees within one test process, so an Action contracted
// by some OTHER plugin's tree would satisfy a registry lookup and the test would pass
// for the wrong reason. A path either exists or it does not.
func TestEveryStepActionIsContracted(t *testing.T) {
	repo := filepath.Join("..", "..", "..")
	contractDirs := []string{filepath.Join(repo, "contracts", "actions")}
	pluginDirs, err := filepath.Glob(filepath.Join(repo, "plugins", "*", "contracts", "actions"))
	if err != nil {
		t.Fatal(err)
	}
	contractDirs = append(contractDirs, pluginDirs...)

	checked := 0
	for _, dir := range estateWorkflowDirs(t, repo) {
		files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var wf struct {
				Steps []struct {
					Name   string `yaml:"name"`
					Action string `yaml:"action"`
				} `yaml:"steps"`
			}
			if err := yaml.Unmarshal(raw, &wf); err != nil {
				t.Errorf("%s: %v", f, err)
				continue
			}
			for _, s := range wf.Steps {
				// A Step naming a capability CLASS rather than a concrete Action resolves
				// through a binding (ADR-0140 D3) and is contracted by the class, not here.
				if s.Action == "" || !strings.Contains(s.Action, "/") {
					continue
				}
				checked++
				if !actionContractExists(contractDirs, s.Action) {
					t.Errorf("%s: step %q names action %q, and no contracts/actions/%s.input.schema.json exists in the repo — "+
						"an uncontracted operation must not exist (§2.2, ADR-0031)", f, s.Name, s.Action, s.Action)
				}
			}
		}
	}
	// Coverage: a glob that silently stopped matching would make this pass having
	// examined nothing, which is the failure mode it exists to catch in others.
	if checked == 0 {
		t.Fatal("no Workflow Step named an Action — the estate layout changed and this guard is checking nothing")
	}
	t.Logf("checked %d Action references", checked)
}

func actionContractExists(dirs []string, action string) bool {
	for _, d := range dirs {
		if _, err := os.Stat(filepath.Join(d, action+".input.schema.json")); err == nil {
			return true
		}
	}
	return false
}

// estateWorkflowDirs returns every workflows/ directory in the repo: the reference
// estate, each plugin's shipped estate, and each plugin's demo estate. A demo tree
// counts — three defects so far have lived in exactly the copy nothing checked.
func estateWorkflowDirs(t *testing.T, repo string) []string {
	t.Helper()
	var out []string
	for _, pat := range []string{
		filepath.Join(repo, "estate", "workflows"),
		filepath.Join(repo, "plugins", "*", "estate", "workflows"),
		filepath.Join(repo, "plugins", "*", "demo", "estate", "workflows"),
		filepath.Join(repo, "demos", "*", "estate", "workflows"),
	} {
		m, err := filepath.Glob(pat)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, m...)
	}
	return out
}
