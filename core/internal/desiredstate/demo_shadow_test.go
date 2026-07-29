package desiredstate

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestDemoWorkflowsDoNotShadowWithDifferentParams refuses the defect-hiding shape this repo has
// now been bitten by three times: ONE artifact under test, TWO copies, and the live-verified copy
// is not the one the estate uses.
//
// A plugin's demo estate legitimately carries its own Workflows — a demo admits the plugin
// `contractsOnly` (ADR-0137 D3) because it wants the seam, not the provisioning story. What it must
// not do is declare a Workflow the plugin's SHIPPED estate also declares, with different bindings.
// That is exactly how PRV-1 hid: `compute-build` existed twice, the demo's copy omitted the two
// keys whose presence broke every real build, and the demo passed while the reference estate could
// not launch. PRV-1's own remediation asked for this guard; it was not written, and PRV-2 then hid
// in the same gap — the demo copy still bound `placement.subnet` after the shipped one moved to
// `placement.subnetRef`.
//
// It compares BINDINGS, not prose. The copies may — and do — explain themselves differently; what
// must not drift is what each one actually sends, and which inputs it accepts.
func TestDemoWorkflowsDoNotShadowWithDifferentParams(t *testing.T) {
	repo := filepath.Join("..", "..", "..")
	shipped, err := filepath.Glob(filepath.Join(repo, "plugins", "*", "estate", "workflows", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(shipped) == 0 {
		t.Fatal("no shipped plugin Workflows found — the layout changed and this guard checks nothing")
	}
	byPlugin := map[string]map[string]string{} // plugin -> workflow name -> path
	for _, p := range shipped {
		plugin := pluginOf(t, p)
		if byPlugin[plugin] == nil {
			byPlugin[plugin] = map[string]string{}
		}
		byPlugin[plugin][workflowName(t, p)] = p
	}

	demos, err := filepath.Glob(filepath.Join(repo, "plugins", "*", "demo", "estate", "workflows", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var compared int
	for _, d := range demos {
		plugin := pluginOf(t, d)
		name := workflowName(t, d)
		s, shadows := byPlugin[plugin][name]
		if !shadows {
			continue // the demo's own Workflow, shadowing nothing
		}
		compared++
		gotIn, gotBind := workflowInterface(t, d)
		wantIn, wantBind := workflowInterface(t, s)
		if !reflect.DeepEqual(gotIn, wantIn) {
			t.Errorf("%s declares inputs %v but the shipped %s declares %v.\n"+
				"Two copies of one Workflow accepting different launch interfaces means the demo "+
				"proves a Workflow the estate does not use — the shape that hid PRV-1 and PRV-2. "+
				"Converge them, or give the demo's copy its own name",
				rel(d), gotIn, rel(s), wantIn)
		}
		if !reflect.DeepEqual(gotBind, wantBind) {
			t.Errorf("%s binds %v but the shipped %s binds %v.\n"+
				"The bindings are the part that breaks: PRV-2 was the shipped copy moving to "+
				"{{.launch.placement.subnetRef}} while this one kept {{.launch.placement.subnet}}, "+
				"so the demo would keep passing against a value the real estate had stopped sending",
				rel(d), gotBind, rel(s), wantBind)
		}
	}
	if compared == 0 {
		t.Log("no demo Workflow currently shadows a shipped one — guard is idle, which is the " +
			"preferred state (one artifact exercised by everything)")
	} else {
		t.Logf("compared %d shadowing demo Workflow(s) against their shipped originals", compared)
	}
}

func rel(p string) string {
	i := len(p)
	for c := 0; i > 0; i-- {
		if p[i-1] == '/' {
			c++
			if c == 5 {
				break
			}
		}
	}
	return p[i:]
}

func pluginOf(t *testing.T, path string) string {
	t.Helper()
	parts := filepath.SplitList(path)
	_ = parts
	// .../plugins/<name>/...
	dir := path
	for dir != "." && dir != "/" {
		parent := filepath.Dir(dir)
		if filepath.Base(parent) == "plugins" {
			return filepath.Base(dir)
		}
		dir = parent
	}
	t.Fatalf("cannot derive plugin name from %s", path)
	return ""
}

func workflowName(t *testing.T, path string) string {
	t.Helper()
	var f struct {
		Name string `yaml:"name"`
	}
	raw, err := os.ReadFile(path) //nolint:gosec // test-only read of a repo path
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, &f); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return f.Name
}

var launchTok = regexp.MustCompile(`\{\{\s*\.launch\.([a-zA-Z0-9_.]+)\s*\}\}`)

// workflowInterface returns a Workflow's declared input NAMES and the sorted set of
// {{.launch.*}} paths its Steps actually bind — the two halves of "what this Workflow accepts
// and what it sends". Comments and descriptions are deliberately excluded.
func workflowInterface(t *testing.T, path string) (inputs, bindings []string) {
	t.Helper()
	var f struct {
		Inputs struct {
			Properties map[string]any `yaml:"properties"`
		} `yaml:"inputs"`
		Steps []map[string]any `yaml:"steps"`
	}
	raw, err := os.ReadFile(path) //nolint:gosec // test-only read of a repo path
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, &f); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	for k := range f.Inputs.Properties {
		inputs = append(inputs, k)
	}
	sort.Strings(inputs)

	seen := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			for _, m := range launchTok.FindAllStringSubmatch(x, -1) {
				seen[m[1]] = true
			}
		case map[string]any:
			for _, val := range x {
				walk(val)
			}
		case []any:
			for _, val := range x {
				walk(val)
			}
		}
	}
	for _, st := range f.Steps {
		walk(st["params"])
		walk(st["inputs"])
	}
	for k := range seen {
		bindings = append(bindings, k)
	}
	sort.Strings(bindings)
	return inputs, bindings
}
