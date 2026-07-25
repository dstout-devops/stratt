package desiredstate

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// taskfilePath is the repo's task runner, read as DATA here rather than executed. The
// reference estate and demos already resolve relative to the same repo root.
const taskfilePath = "../../../Taskfile.yml"

// This file is ADR-0117 follow-up (l)'s residual, and (l) named it precisely: an EE-Job
// Actuator is enabled WITHOUT any image check — enableActuatorLocked skips the dial for a
// jobCommand Actuator, because there is nothing to dial. So a declaration naming an image
// that no build produces reports HEALTHY, and stays healthy, until its first Run fails
// ~2 minutes later in the dispatcher. (l) fixed the diagnosis at run time; it could not fix
// the fact that the estate had no way to be wrong about an image before then.
//
// The cheap, honest gate (l) booked is this one: tie every declared image to a build task.
//
// It asserts two halves, and the second is the one that actually bites:
//
//  1. The image is built SOMEWHERE in the Taskfile. Catches a typo'd tag and a declaration
//     that outlived the task that produced it.
//  2. For a demo, the image is built by that demo's OWN `demo:<name>:run` task, transitively.
//     A tag that some unrelated task builds is not a working demo — it is a demo that passes
//     on the maintainer's machine because they happened to build the image last week, and
//     fails on a fresh clone. demo:app-cert:run already builds dev:ee-crypto:build for exactly
//     this reason, with a comment citing (l); this pins that it stays true.
//
// What it deliberately does NOT cover, so the coverage claim stays honest:
//   - An Actuator with NO image falls back to the floor's cfg.EEImage (helm's stratt-ee
//     repository/tag), which is a chart value rather than an estate declaration.
//   - Whether the image was actually BUILT — that needs a docker daemon, which `task ci`
//     must not need. This checks that a build exists to be run, not that it has been.
//   - The reference estate has no per-demo run task, so only half 1 applies to it.
func TestEveryDeclaredActuatorImageIsBuiltByATask(t *testing.T) {
	tf := parseTaskfile(t)
	built := map[string][]string{} // image -> tasks that build it
	for name := range tf.Tasks {
		for _, img := range imagesBuiltBy(tf, name, nil) {
			built[img] = append(built[img], name)
		}
	}
	if len(built) == 0 {
		t.Fatal("parsed no `docker build -t <image>` out of the Taskfile — the parser is broken, " +
			"and a guard that finds nothing passes everything")
	}

	trees := map[string]string{"reference": estateRoot}
	dirs, err := filepath.Glob(filepath.Join(demosRoot, "*", "estate"))
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range dirs {
		trees["demo/"+filepath.Base(filepath.Dir(dir))] = dir
	}

	checked := 0
	for label, dir := range trees {
		t.Run(label, func(t *testing.T) {
			if _, err := os.Stat(dir); err != nil {
				t.Skipf("estate not found at %s (%v)", dir, err)
			}
			decls, err := ParseDir(dir, nil)
			if err != nil {
				t.Fatalf("%s does not parse/validate: %v", dir, err)
			}
			for _, a := range decls.Actuators {
				if a.Image == "" {
					continue // falls back to the floor's EE image — a chart value, not an estate one
				}
				checked++
				if len(built[a.Image]) == 0 {
					t.Errorf("Actuator %q declares image %q, which NO Taskfile task builds — the floor will "+
						"report it healthy (an EE-Job Actuator is never dialled) and its first Run will fail "+
						"in the dispatcher instead. Build it from a task, or fix the tag. Known images: %v",
						a.Name, a.Image, builtImageNames(built))
					continue
				}
				demo, ok := strings.CutPrefix(label, "demo/")
				if !ok {
					continue
				}
				runTask := "demo:" + demo + ":run"
				if _, ok := tf.Tasks[runTask]; !ok {
					t.Errorf("demo %q has an estate but no %q task — nothing stands it up", demo, runTask)
					continue
				}
				if !slices.Contains(imagesBuiltBy(tf, runTask, nil), a.Image) {
					t.Errorf("%s declares Actuator %q on image %q, but %s never builds it (only %v do) — "+
						"the demo would pass only on a machine that happens to have the image already, "+
						"and fail on a fresh clone with a dispatcher error two minutes into the run",
						dir, a.Name, a.Image, runTask, built[a.Image])
				}
			}
		})
	}
	if checked == 0 {
		t.Error("no Actuator in any estate declares an image — this guard passed by finding nothing. " +
			"If the last per-Step EE declaration was removed (ADR-0117 D3a), delete this test rather " +
			"than leaving it green and empty")
	}
}

// --- Taskfile as data -------------------------------------------------------------------

type taskfileDoc struct {
	Tasks map[string]struct {
		Deps []taskStep `yaml:"deps"`
		Cmds []taskStep `yaml:"cmds"`
	} `yaml:"tasks"`
}

// taskStep is one entry of `deps:`/`cmds:`, which Task allows as either a bare string or a
// mapping. Only the two members that matter here are modelled; every other shape (`for:`,
// `vars:`, `defer:`) decodes to an empty step and is ignored rather than failing the parse.
type taskStep struct {
	Cmd  string
	Task string
}

func (s *taskStep) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&s.Cmd)
	}
	var m struct {
		Cmd  string `yaml:"cmd"`
		Task string `yaml:"task"`
	}
	if err := n.Decode(&m); err != nil {
		return nil //nolint:nilerr // an unmodelled step shape is not this guard's business
	}
	s.Cmd, s.Task = m.Cmd, m.Task
	return nil
}

func parseTaskfile(t *testing.T) taskfileDoc {
	t.Helper()
	b, err := os.ReadFile(taskfilePath)
	if err != nil {
		t.Fatalf("cannot read %s: %v", taskfilePath, err)
	}
	var doc taskfileDoc
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("%s does not parse as YAML: %v", taskfilePath, err)
	}
	if len(doc.Tasks) == 0 {
		t.Fatalf("%s declares no tasks — the parser is out of step with the file's shape", taskfilePath)
	}
	return doc
}

// dockerBuildTag matches the `-t <image>` of a docker build. Anchored on a tagged reference
// (name:tag) so a bare `-t` flag of some other command cannot masquerade as an image.
var dockerBuildTag = regexp.MustCompile(`-t\s+"?([A-Za-z0-9._/-]+:[A-Za-z0-9._-]+)"?`)

// imagesBuiltBy walks a task and everything it invokes, returning every image built along the
// way. Recursion is over `deps:` and `cmds:` alike, since Task treats a `task:` step in either
// as an invocation; `seen` makes a cyclic or diamond graph terminate.
func imagesBuiltBy(tf taskfileDoc, name string, seen map[string]bool) []string {
	if seen == nil {
		seen = map[string]bool{}
	}
	if seen[name] {
		return nil
	}
	seen[name] = true
	task, ok := tf.Tasks[name]
	if !ok {
		return nil
	}
	var out []string
	for _, step := range append(append([]taskStep{}, task.Deps...), task.Cmds...) {
		if step.Task != "" {
			out = append(out, imagesBuiltBy(tf, step.Task, seen)...)
		}
		if step.Cmd == "" {
			continue
		}
		// Join shell line-continuations first: a multi-line `|` command splits one
		// docker build across several lines, with the -t on a line of its own.
		for _, line := range strings.Split(strings.ReplaceAll(step.Cmd, "\\\n", " "), "\n") {
			if !strings.Contains(line, "docker build") {
				continue
			}
			if m := dockerBuildTag.FindStringSubmatch(line); m != nil {
				out = append(out, m[1])
			}
		}
	}
	return out
}

func builtImageNames(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
