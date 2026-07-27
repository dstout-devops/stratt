package desiredstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestDemoEstatesAdmitThePluginsTheyUse refuses a demo estate that dispatches a
// plugin-owned Action, or declares an Actuator against a plugin identity, without
// ADMITTING that plugin in its own plugins.yaml.
//
// Since ADR-0138 D3/D4, a plugin's Contracts live in the plugin's tree and core holds
// only what the estate admits. Core cannot learn them from the plugin at runtime
// either: the Manifest carries a schema id and a hash, never the schema (§1.5 — a
// plugin does not mint contract meaning). So an unadmitted plugin's Action has no
// input Contract on that floor, and the failure lands at the far end of a gate an
// operator has just approved.
//
// WHY A TEST AND NOT A LOAD-TIME CHECK, again: parsing a demo estate FROM THE REPO
// resolves the plugin tree whether or not the demo admits it, and the process-wide
// contract registry accumulates across trees — so `task ci` stayed green while the
// vsphere demo floor could not boot at all. Only running the demo caught that, weeks
// after ADR-0103 moved vcenter's Actions into a declaration. This makes the same
// omission fail in seconds instead.
func TestDemoEstatesAdmitThePluginsTheyUse(t *testing.T) {
	repo := filepath.Join("..", "..", "..")
	// The package's own census of demo estates, so a demo that moves home cannot drop
	// out of this guard while the coverage still looks intact (see demoEstates).
	roots := demoEstates(t)
	if len(roots) == 0 {
		t.Fatal("no demo estates found — the layout changed and this guard is checking nothing")
	}

	for _, root := range roots {
		t.Run(demoLabel(root), func(t *testing.T) {
			admitted := admittedNames(t, filepath.Join(root, "plugins.yaml"))
			for plugin, why := range pluginContractsUsedBy(t, repo, root) {
				if !admitted[plugin] {
					t.Errorf("%s uses %s, whose Contract lives in the %s plugin's tree (ADR-0138 D3/D4), "+
						"but its plugins.yaml does not admit %q — on a real floor core would not hold that "+
						"document and the Step fails after the gate is approved. Admit it, contractsOnly.",
						root, why, plugin, plugin)
				}
			}
		})
	}
}

// pluginContractsUsedBy returns plugin name → the reference that needs it.
func pluginContractsUsedBy(t *testing.T, repo, root string) map[string]string {
	t.Helper()
	need := map[string]string{}

	for _, f := range yamlsIn(t, filepath.Join(root, "workflows")) {
		var wf struct {
			Steps []struct {
				Action string `yaml:"action"`
			} `yaml:"steps"`
		}
		decodeYAML(t, f, &wf)
		for _, s := range wf.Steps {
			ns, _, ok := strings.Cut(s.Action, "/")
			if !ok {
				continue
			}
			if exists(filepath.Join(repo, "plugins", ns, "contracts", "actions", s.Action+".input.schema.json")) {
				need[ns] = "action " + s.Action
			}
		}
	}

	// An Actuator names a plugin IDENTITY, and its input Contract lives in that
	// plugin's tree under the identity's name — app-cert's `ansible-crypto` with
	// pluginIdentity: ansible is the case that first needed admitting.
	for _, f := range yamlsIn(t, filepath.Join(root, "actuators")) {
		var act struct {
			Name           string `yaml:"name"`
			PluginIdentity string `yaml:"pluginIdentity"`
		}
		decodeYAML(t, f, &act)
		id := act.PluginIdentity
		if id == "" {
			continue
		}
		if exists(filepath.Join(repo, "plugins", id, "contracts", "actuators", id+".input.schema.json")) {
			need[id] = "actuator " + act.Name
		}
	}
	return need
}

func admittedNames(t *testing.T, path string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return out
	}
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Plugins []struct {
			Name string `yaml:"name"`
		} `yaml:"plugins"`
	}
	if err := yaml.Unmarshal(raw, &f); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	for _, p := range f.Plugins {
		out[p.Name] = true
	}
	return out
}

func yamlsIn(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func decodeYAML(t *testing.T, path string, into any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, into); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
