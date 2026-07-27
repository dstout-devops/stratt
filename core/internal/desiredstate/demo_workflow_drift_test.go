package desiredstate

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestPluginDemoWorkflowsMatchShipped refuses a plugin demo that declares a Workflow of the
// same NAME as one the plugin itself ships, with a different Step param shape.
//
// This is a repo-hygiene guard on a defect class that has now cost real time three times, in
// three different shapes:
//
//   - `awxsim` served only the adopt deep-read's endpoints, so the projection half of one
//     module was exercised by a simulator of different breadth (ADR-0128 / the AWX audit §4);
//   - the `web-server` Blueprint's capability route resolved through a fake in every unit
//     test and broke every real floor — only `dev:connector-e2e` caught it;
//   - **PRV-1**: `plugins/awsec2/estate/workflows/compute-build.yaml` sent `subnet` and
//     `availabilityZone` into an `additionalProperties:false` Contract, so every
//     Intent/Compute build failed at launch — while `plugins/awsec2/demo/estate/workflows/
//     compute-build.yaml`, the copy that is actually live-verified, omitted exactly those two
//     params and passed. The demo proved a Workflow the estate does not use.
//
// The shared shape is a DIVERGENT SECOND COPY of the thing under test. The copy is not always
// wrong — a demo estate must be self-contained (ADR-0116 D1), and a plugin's demo is staged by
// replacing the declarations mount, so it genuinely cannot import the plugin's shipped estate
// today. What must not happen is the two drifting in the one dimension that decides whether the
// shipped one works.
//
// Deliberately narrow, so it stays a guard and not a straitjacket: it compares only the SET of
// param keys per Step, only for same-named Workflows, only within one plugin. Values, step
// ordering, gates and extra steps are all free to differ — a demo legitimately tunes those.
func TestPluginDemoWorkflowsMatchShipped(t *testing.T) {
	plugins, err := filepath.Glob(filepath.Join("..", "..", "..", "plugins", "*"))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, p := range plugins {
		shipped := workflowParamKeys(t, filepath.Join(p, "estate", "workflows"))
		demo := workflowParamKeys(t, filepath.Join(p, "demo", "estate", "workflows"))
		for name, demoSteps := range demo {
			shippedSteps, both := shipped[name]
			if !both {
				continue // a demo-only Workflow is fine; there is nothing to diverge from
			}
			checked++
			for step, demoKeys := range demoSteps {
				shippedKeys, ok := shippedSteps[step]
				if !ok {
					continue // a demo may add steps
				}
				if strings.Join(demoKeys, " ") != strings.Join(shippedKeys, " ") {
					t.Errorf("%s: Workflow %q step %q param keys DIVERGE between the plugin's shipped estate and its demo\n"+
						"  shipped: [%s]\n"+
						"  demo:    [%s]\n"+
						"The demo is what gets live-verified, so a divergence here means the demo proves a Workflow "+
						"the estate does not use — exactly how PRV-1 stayed hidden. Make them match, or give the demo "+
						"Workflow its own name.",
						filepath.Base(p), name, step, strings.Join(shippedKeys, " "), strings.Join(demoKeys, " "))
				}
			}
		}
	}
	if checked == 0 {
		t.Skip("no plugin ships both an estate Workflow and a demo Workflow of the same name")
	}
	t.Logf("compared %d same-named shipped/demo Workflows", checked)
}

// workflowParamKeys reads a workflows dir into workflow name → step name → sorted param keys.
// Absent dirs yield an empty map; this is a hygiene check, not a structural requirement that
// every plugin have either tree.
func workflowParamKeys(t *testing.T, dir string) map[string]map[string][]string {
	t.Helper()
	out := map[string]map[string][]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", filepath.Join(dir, e.Name()), err)
		}
		var wf struct {
			Name  string `yaml:"name"`
			Steps []struct {
				Name   string         `yaml:"name"`
				Params map[string]any `yaml:"params"`
			} `yaml:"steps"`
		}
		if err := yaml.Unmarshal(raw, &wf); err != nil || wf.Name == "" {
			continue // not a Workflow document; other validators own that complaint
		}
		steps := map[string][]string{}
		for _, s := range wf.Steps {
			keys := make([]string, 0, len(s.Params))
			for k := range s.Params {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			steps[s.Name] = keys
		}
		out[wf.Name] = steps
	}
	return out
}
