package desiredstate

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/dstout-devops/stratt/types"
)

// Tool content beside the estate (ADR-0134): an Actuator DECLARES a content root
// (`contentDir`, a path relative to the estate root) and the estate load resolves it into
// types.Actuator.Content — relative path → file content — which executeJobPlugin later merges
// into JobSpec.Files under project/.
//
// Resolution happens HERE, at parse time, and not at dispatch (D3). JobSpec.Files is
// remote-safe (ADR-0032), so resolved content travels with the JobSpec to a Site, which a
// dispatch-time filesystem read could not do — the estate mount does not exist there. It also
// puts a playbook edit into `stratt plan`'s diff, where a change to what will run belongs, and
// it keeps dispatch filesystem-free.
//
// Nothing in this file knows what a playbook is. It copies a directory an Actuator declared
// (§1.4); the only judgements it makes are about SIZE, SHAPE and REACH — the three ways a
// content root fails at a layer that cannot explain itself.

const (
	// maxContentBytes bounds a resolved content root. The tree becomes a ConfigMap and
	// Kubernetes caps a ConfigMap at 1 MiB; half of that is the conservative ceiling,
	// leaving room for the request.json and inventory the same ConfigMap carries. Refused
	// here, naming the directory and the size, rather than admitted into a Job that fails
	// to schedule for reasons nobody can see (§1.8).
	//
	// Per-project scoping (D1) is what makes this comfortable: a Run mounts ONE project,
	// never every project in the estate.
	maxContentBytes = 512 << 10
	// maxContentFiles bounds the file COUNT, which is a separate limit from the byte
	// ceiling and fails in a different place: the dispatcher emits one VolumeMount per
	// JobSpec.Files entry, so a tree of thousands of small files produces a pod spec that
	// is rejected or unreadable while every individual file is tiny. Discovered by reading
	// createJob rather than by hitting it in production, and bounded for the same §1.8
	// reason as the byte ceiling.
	maxContentFiles = 256
)

// contentSegment is one legal path segment inside a content root: it must begin with an
// alphanumeric or underscore, which makes ".." unrepresentable and keeps the flattened
// ConfigMap key (dispatch.cmKey replaces "/" with "__") inside the [-._a-zA-Z0-9]+ K8s
// allows. The same rule the ansible.input.v7 `playbook` pattern states, applied to the
// files rather than to the reference — so a file that could never be mounted is refused
// where a human is reading a diff, not where a Job is failing to start.
var contentSegment = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// validateContentDir checks the SHAPE of a declared contentDir. Reach is the concern: the
// path is joined onto the estate root, so an absolute path or a `..` segment would read a
// tree the estate does not own.
func validateContentDir(actuator, dir string) error {
	if dir == "" {
		return nil
	}
	if filepath.IsAbs(dir) || strings.HasPrefix(dir, "/") {
		return fmt.Errorf("actuator %q: contentDir %q must be relative to the estate root, not absolute", actuator, dir)
	}
	for _, seg := range strings.Split(filepath.ToSlash(dir), "/") {
		if !contentSegment.MatchString(seg) {
			return fmt.Errorf("actuator %q: contentDir %q has an illegal path segment %q — each segment must begin with an alphanumeric or underscore, which is what keeps a content root inside the estate", actuator, dir, seg)
		}
	}
	return nil
}

// resolveActuatorContent reads each Actuator's declared contentDir into its Content map,
// keyed by path relative to that directory. Called once per estate load, after the Actuator
// declarations parse — parseKind hands its parser only (path, raw), so the estate ROOT is
// available here and not there.
// resolveActuatorContent reads one Actuator's declared content root into its
// Content map. `root` is the estate that SHIPPED this Actuator — its own plugin
// estate when it came from one (ADR-0137 D1), the estate root otherwise.
func resolveActuatorContent(root string, a *types.Actuator) error {
	if a.ContentDir == "" {
		return nil
	}
	files, err := readContentDir(root, a.Name, a.ContentDir)
	if err != nil {
		return err
	}
	a.Content = files
	return nil
}

// readContentDir walks one content root into a relpath → content map, refusing anything that
// would fail later somewhere less legible: a missing directory, an empty one, a symlink, a
// path segment K8s cannot key, too many files, or too many bytes.
func readContentDir(root, actuator, contentDir string) (map[string]string, error) {
	dir := filepath.Join(root, filepath.FromSlash(contentDir))
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("desiredstate: actuator %q: contentDir %s: %w", actuator, contentDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("desiredstate: actuator %q: contentDir %s is not a directory", actuator, contentDir)
	}
	files := map[string]string{}
	total := 0
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		for _, seg := range strings.Split(rel, "/") {
			if !contentSegment.MatchString(seg) {
				return fmt.Errorf("%s: illegal path segment %q — a mounted file's path becomes a ConfigMap key, which K8s restricts to [-._a-zA-Z0-9]", rel, seg)
			}
		}
		if d.IsDir() {
			return nil
		}
		// A symlink is refused rather than followed: os.ReadFile would resolve it, so a
		// link inside a reviewed estate could pull an unreviewed file — /etc/passwd, a
		// key — into a ConfigMap mounted in an execution pod. Git stores the link, not
		// the target, so a reviewer sees a one-line file and not what it reads.
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s: is a symlink — content must be a real file, so what a reviewer reads is what a Run mounts", rel)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: is not a regular file", rel)
		}
		// play.yml at the root of a content tree is RESERVED, because the shim's inline
		// path writes params.play to exactly that path. An Actuator can legitimately serve
		// both — a project for the Workflows that need one, an inline guard play for the
		// one that does not — and the two collide on this single name: the inline write
		// would land on a read-only mount and fail with a bare EACCES in a pod. Refused
		// here, where the name is visible and renaming it is free (§1.8).
		if rel == "play.yml" {
			return fmt.Errorf("play.yml is a reserved name at the root of a content root — the shim writes an inline params.play there, so a mounted file of that name would collide with it; rename this playbook")
		}
		b, ferr := os.ReadFile(path)
		if ferr != nil {
			return ferr
		}
		total += len(b)
		if len(files) >= maxContentFiles {
			return fmt.Errorf("holds more than %d files — the dispatcher mounts one volume per file, so a larger tree produces a pod spec that fails to schedule for reasons nothing surfaces", maxContentFiles)
		}
		if total > maxContentBytes {
			return fmt.Errorf("is at least %d bytes, over the %d-byte ceiling — the mounted tree becomes a ConfigMap and K8s caps one at 1 MiB", total, maxContentBytes)
		}
		files[rel] = string(b)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("desiredstate: actuator %q: contentDir %s: %w", actuator, contentDir, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("desiredstate: actuator %q: contentDir %s holds no files — an Actuator that declares a content root and mounts nothing runs whatever the EE image happens to contain", actuator, contentDir)
	}
	return files, nil
}

// stringAtPath walks a dotted path through a params map and returns the string there, if any.
// The read-only sibling of policy.truthyAtPath, and deliberately the same shape: core walks a
// path something DECLARED and never a key it knows the name of.
func stringAtPath(params map[string]any, path string) string {
	cur := any(params)
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		if cur, ok = m[seg]; !ok {
			return ""
		}
	}
	s, _ := cur.(string)
	return s
}

// validateContentRefs checks that every Step or Trigger naming a file within its Actuator's
// content root names one the resolved tree actually holds — moving the failure from a 3 a.m.
// Run to a failed load (§1.8).
//
// WHICH PARAM holds that name is DECLARED, on the Actuator's contentInputs, and is never a key
// this function knows. That is the whole §1.4 point: core reads a declared path and asks a map
// whether it has that key, exactly as ElevatedInputs lets it derive a privilege label without
// learning the word `become`. Reading `params["playbook"]` here would put ansible awareness
// into the estate loader — the same mistake, one layer up, that ADR-0117 D3a keeps out of
// dispatch.
//
// EXISTENCE ONLY. Core never opens the file and never parses a play (ADR-0117 D6);
// variable-binding checks stay in tests, where tool awareness is allowed.
//
// The honest limit: this runs on an estate LOAD, where the root and its content are in hand.
// A Workflow posted to the API is validated with only the Actuator identity map (no estate
// root), so it does not get this check — the same shape as every other load-time estate
// check, and not a gap this ADR introduces.
func validateContentRefs(actuators []types.Actuator, workflows []types.Workflow, triggers []types.Trigger) error {
	byName := make(map[string]types.Actuator, len(actuators))
	for _, a := range actuators {
		byName[a.Name] = a
	}
	check := func(what, actuator string, params map[string]any) error {
		a, declared := byName[actuator]
		if !declared {
			// An undeclared Actuator is somebody else's error to report (the name may be
			// boot-registered), and guessing here would produce a second, wronger message.
			return nil
		}
		for _, path := range a.ContentInputs {
			ref := stringAtPath(params, path)
			if ref == "" {
				continue
			}
			if _, ok := a.Content[ref]; !ok {
				return fmt.Errorf("desiredstate: %s sets %s to %q, which is not in actuator %q's content root %s (holds: %s)", what, path, ref, actuator, a.ContentDir, strings.Join(sortedContentPaths(a.Content), ", "))
			}
		}
		return nil
	}
	for _, wf := range workflows {
		for _, st := range wf.Steps {
			if err := check(fmt.Sprintf("workflow %q step %q", wf.Name, st.Name), st.Actuator, st.Params); err != nil {
				return err
			}
		}
	}
	for _, tr := range triggers {
		if err := check(fmt.Sprintf("trigger %q", tr.Name), tr.Actuator, tr.Params); err != nil {
			return err
		}
	}
	return nil
}

// sortedContentPaths lists a resolved tree's paths, for deterministic diagnostics — a
// "which playbooks ARE there" list is the difference between a failure that ends the
// question and one that starts an investigation (§1.8).
func sortedContentPaths(content map[string]string) []string {
	paths := make([]string, 0, len(content))
	for p := range content {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}
