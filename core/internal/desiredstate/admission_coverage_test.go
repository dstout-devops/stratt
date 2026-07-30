package desiredstate

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/policy"
	"github.com/dstout-devops/stratt/types"
)

// recordingDecider allows everything and records the `kind` of every object it was asked
// about. It is how the two admission doors are compared BEHAVIOURALLY rather than by
// eyeballing two lists in two functions — which is exactly how they drifted apart.
type recordingDecider struct {
	mu    sync.Mutex
	kinds map[string]bool
}

func newRecordingDecider() *recordingDecider {
	return &recordingDecider{kinds: map[string]bool{}}
}

func (r *recordingDecider) Admit(_ context.Context, req policy.AdmissionRequest) types.Decision {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, _ := req.Object["kind"].(string)
	r.kinds[k] = true
	return types.Decision{Outcome: types.OutcomeAllow}
}

func (r *recordingDecider) Decide(_ context.Context, _ policy.Request) types.Decision {
	return types.Decision{Outcome: types.OutcomeAllow}
}
func (r *recordingDecider) Validate(_ []types.Control) error { return nil }

func (r *recordingDecider) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.kinds))
	for k := range r.kinds {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// TestAdmissionJudgesEveryDeclarationDirectory refuses an estate directory that admission
// does not walk.
//
// admissionDirs used to list fifteen of the twenty-two directories the estate can carry, and
// the omissions were not the harmless ones: capability-bindings/ is the single place a
// provider or a substrate may be named (ADR-0151) — the line whose edit migrates a whole
// topology — and actuators/ + connectors/ are the L0 grant surface, what a plugin is allowed
// to do. Neither could be matched by any control.
//
// The failure mode is the one §1.8 is about: a control that never fires is indistinguishable
// from a control that always passes. An org writes `object.kind == 'CapabilityBinding'`,
// watches the estate load green, and concludes the rule holds.
//
// admission/ is excluded by design — a policy does not admit itself.
func TestAdmissionJudgesEveryDeclarationDirectory(t *testing.T) {
	root := filepath.Join("..", "..", "..", "estate")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read estate: %v", err)
	}
	var found int
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "admission" {
			continue
		}
		found++
		if !slices.Contains(admissionDirs, e.Name()) {
			t.Errorf("estate/%s/ is a declaration directory that admission does NOT judge — add it "+
				"to admissionDirs. A control matching its kind would load, validate, and never fire, "+
				"which reads exactly like a control that always passes (§1.8)", e.Name())
		}
	}
	if found == 0 {
		t.Fatal("no estate directories found — the layout moved and this guard is checking nothing")
	}
}

// TestEveryJudgedDirectoryHasAKindToMatchOn refuses an admitted directory with no entry in
// dirKindByDir.
//
// The fallback used to be `default: return sub`, so half the directories were admitted under
// their plural directory name while the imperative door used the singular type name for the
// same declaration. Requiring an explicit entry makes the token a DECISION rather than an
// accident of which switch arm someone remembered to write.
func TestEveryJudgedDirectoryHasAKindToMatchOn(t *testing.T) {
	for _, sub := range admissionDirs {
		if _, ok := dirKindByDir[sub]; !ok {
			t.Errorf("admissionDirs judges %q but dirKindByDir has no entry for it — it would be "+
				"admitted under its raw directory name, which a control author has no reason to "+
				"guess and which the imperative door does not use", sub)
		}
	}
}

// TestBothAdmissionDoorsAgreeOnKind runs the REAL estate through both admission doors and
// compares the kinds each one presented to the PDP.
//
// The doors are meant to be interchangeable — ADR-0073's whole point is one policy, both
// paths — and they were not. AdmitDeclarations (POST /desired-state/apply) omitted
// CredentialRef and SCIMIdP, which the Git door had always judged, so a control over a
// credential pointer held on a reconcile and silently did not hold on an API write. That is
// the GOV-2 bypass this function exists to CLOSE, reopened from the other side.
//
// The three list-shaped directories are exempt: authz/, advisories/ and hosts/ have no typed
// counterpart in Declarations (they are read by the authz reconcile, the advisory check and
// the declared-estate plugin), so the Git door judges the file and the imperative door has
// nothing to present. Stated here rather than papered over — if one of them grows a typed
// declaration, this list is where the omission surfaces.
func TestBothAdmissionDoorsAgreeOnKind(t *testing.T) {
	root := filepath.Join("..", "..", "..", "estate")

	gitDoor := newRecordingDecider()
	roots, err := estateRoots(root)
	if err != nil {
		t.Fatalf("estate roots: %v", err)
	}
	// One control, allow-shaped: admitEstate only walks when the estate declares a policy, and
	// the recording decider is what actually observes each object.
	ctrls := []types.Control{{ID: "observe", When: "true", Outcome: types.OutcomeAllow}}
	if err := admitEstate(roots, ctrls, gitDoor); err != nil {
		t.Fatalf("git-door admission: %v", err)
	}

	decls, err := ParseDir(root, policy.Bypass{})
	if err != nil {
		t.Fatalf("parse estate: %v", err)
	}
	apiDoor := newRecordingDecider()
	if err := AdmitDeclarations(context.Background(), decls, ctrls, apiDoor); err != nil {
		t.Fatalf("imperative-door admission: %v", err)
	}

	// Directories whose manifest is a list document with no typed Declarations field.
	fileOnly := map[string]bool{"authz": true, "advisories": true, "hosts": true}
	api := map[string]bool{}
	for _, k := range apiDoor.seen() {
		api[k] = true
	}
	for _, k := range gitDoor.seen() {
		if fileOnly[k] {
			continue
		}
		// An Intent manifest carries its own sub-kind (Intent/Compute), and so does the typed
		// Intent — both doors agree by construction, and neither emits the bare "Intent"
		// fallback for the reference estate. Compare on the prefix so a sub-kind is not read
		// as a disagreement.
		if strings.HasPrefix(k, "Intent/") {
			continue
		}
		if !api[k] {
			t.Errorf("the Git door admits kind %q and the imperative door does not — a control over "+
				"it holds on a reconcile and silently does not hold on POST /desired-state/apply, "+
				"which is the GOV-2 bypass AdmitDeclarations exists to close (ADR-0073)", k)
		}
	}
	if len(gitDoor.seen()) == 0 {
		t.Fatal("the git door observed nothing — the estate moved and this guard is checking nothing")
	}
}

// TestAdmissionJudgesPluginRoots proves a plugin-shipped declaration reaches the PDP.
//
// admitEstate took a single `root` while every kind below it is parsed across the estate root
// AND every plugin root admitted in plugins.yaml (ADR-0137 D1/D3). So a plugin's Workflows,
// Actuators, Connectors and Blueprints were judged by nothing at all — the exact inverse of
// what admission is for. An org's own manifests were policed; the third-party ones, which are
// the reason to have a policy, were not.
func TestAdmissionJudgesPluginRoots(t *testing.T) {
	dir := t.TempDir()
	estate := filepath.Join(dir, "estate")
	plugin := filepath.Join(dir, "plugin")
	mkdirs(t, filepath.Join(estate, "admission"), filepath.Join(plugin, "workflows"))

	write(t, filepath.Join(estate, "admission", "policy.yaml"),
		"name: p\ncontrols:\n  - id: no-forbidden-workflows\n"+
			"    when: \"object.kind == 'Workflow' && has(object.name) && object.name.startsWith('forbidden-')\"\n"+
			"    outcome: deny\n")
	// A plugin path is resolved relative to the estate root (ADR-0137 D1).
	write(t, filepath.Join(estate, "plugins.yaml"),
		"plugins:\n  - name: demo\n    path: ../plugin\n")
	write(t, filepath.Join(plugin, "workflows", "wf.yaml"),
		"name: forbidden-drop-prod\nsteps:\n  - name: gate\n    gate:\n      approvers:\n        principals: [alice]\n")

	_, err := ParseDir(estate, policy.CEL{})
	if err == nil {
		t.Fatal("a plugin-shipped Workflow the estate's admission policy denies must be REFUSED — " +
			"plugin trees are the third-party surface admission exists for")
	}
	if !strings.Contains(err.Error(), "admission denied") {
		t.Fatalf("want an admission-denied error naming the plugin's file, got: %v", err)
	}
	if !strings.Contains(err.Error(), plugin) {
		t.Fatalf("the error must name the ROOT the denied file came from — an estate's own Workflow "+
			"and a plugin's are different problems with different fixes (§1.8). Got: %v", err)
	}
}

func mkdirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
