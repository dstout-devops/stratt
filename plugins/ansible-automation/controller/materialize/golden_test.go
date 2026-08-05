package materialize

import (
	"context"
	"flag"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	awx "github.com/dstout-devops/stratt/plugins/ansible-automation/controller/awxapi"
	"github.com/dstout-devops/stratt/plugins/ansible-automation/controller/awxapi/awxsim"
)

var update = flag.Bool("update", false, "regenerate the golden adopt bundle in testdata/golden")

const goldenDir = "testdata/golden"

// emitFromSim enumerates the canned awxsim estate through the REAL rich client and runs the
// REAL transform — so the golden is the plugin's actual emission, never hand-authored. The sim
// estate covers every emit shape (Views, job-template + workflow Workflows, gate + actuation
// Steps, CredentialRefs, survey→Contract, the residual report).
func emitFromSim(t *testing.T) map[string]string {
	t.Helper()
	sim := awxsim.New("")
	srv := httptest.NewServer(sim.Handler())
	t.Cleanup(srv.Close)
	sim.SetBase(srv.URL)
	c := awx.New(awx.Config{Endpoint: srv.URL, Token: "sim-token", HTTPClient: srv.Client()})
	snap, err := c.Enumerate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshotBreadth(t, snap)
	emit, err := Bundle(snap, Options{})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"migration-report.md": emit.Report}
	for p, content := range emit.Files {
		files[p] = content
	}
	return files
}

// TestGoldenBundle is the §1.5 CaC contract fixture (ADR-0089 must-fix): the plugin's REAL
// emitter runs against awxsim and its output IS the committed golden bundle in testdata/golden.
// Run `go test -run TestGoldenBundle -update` to regenerate after an INTENDED emit change; CI
// runs WITHOUT -update, so any drift fails here — and the sibling core desiredstate contract
// test (core/internal/desiredstate) proves the same golden still parses through the core CaC
// reader. Two guards across the module boundary; drift is never silently absorbed.
func TestGoldenBundle(t *testing.T) {
	files := emitFromSim(t)

	if *update {
		_ = os.RemoveAll(goldenDir)
		for rel, content := range files {
			full := filepath.Join(goldenDir, rel)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("regenerated %d golden files in %s", len(files), goldenDir)
		return
	}

	// Compare committed golden ⇔ live emission, both directions.
	committed := map[string]bool{}
	_ = filepath.Walk(goldenDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(goldenDir, path)
		committed[rel] = true
		return nil
	})
	if len(committed) == 0 {
		t.Fatalf("no golden fixture in %s — generate it: go test -run TestGoldenBundle -update", goldenDir)
	}
	for rel, want := range files {
		got, err := os.ReadFile(filepath.Join(goldenDir, rel))
		if err != nil {
			t.Errorf("golden missing %s — an emit shape was added; regenerate with -update", rel)
			continue
		}
		if string(got) != want {
			t.Errorf("golden drift in %s — emitter output changed; regenerate with -update and re-run the core contract test", rel)
		}
		delete(committed, rel)
	}
	for rel := range committed {
		t.Errorf("stale golden %s — an emit shape was removed; regenerate with -update", rel)
	}
}

// TestNoBannedVocabularyInDeclarations guards §2: no banned core-model identifier leaks into an
// emitted YAML declaration. AWX object nouns are allowed only in the report prose and in
// awx.*/ansible.* provenance labels — never as a Stratt declaration key/value.
func TestNoBannedVocabularyInDeclarations(t *testing.T) {
	files := emitFromSim(t)
	banned := []string{"job_template", "jobtemplate", "job template", "cmdb"}
	for path, doc := range files {
		if strings.HasSuffix(path, ".md") {
			continue // the report prose may name AWX objects (the "was:" lineage)
		}
		low := strings.ToLower(doc)
		for _, b := range banned {
			if strings.Contains(low, b) {
				t.Errorf("banned vocabulary %q leaked into %s", b, path)
			}
		}
	}
}

// assertSnapshotBreadth pins ADR-0089 D6 property 2 — "coverage-complete: the fixture exercises
// every emit shape" — which was written as a must-fix and, until now, asserted only in prose.
//
// ── WHY HERE AND NOT IN THE DRIFT COMPARISON ─────────────────────────────────────────────────
//
// The golden comparison below catches a change in what the transform EMITS. It cannot catch a
// narrowing of what the transform is FED: a narrowed Enumerate regenerated with -update produces a
// smaller golden that is perfectly self-consistent, so every file matches and the suite stays green
// while the round-trip contract quietly covers a happy-path subset.
//
// That is also the guard that makes enumerate.go's "looks dead, is not" comment enforceable rather
// than hopeful: swapping Enumerate for a single-template read fails HERE, with a message naming the
// ADR, before anything downstream has a chance to look fine.
func assertSnapshotBreadth(t *testing.T, snap *awx.Snapshot) {
	t.Helper()
	for _, c := range []struct {
		what string
		got  int
		min  int
	}{
		{"job templates", len(snap.JobTemplates), 2},
		{"workflow job templates", len(snap.WorkflowJTs), 1},
		{"inventories", len(snap.Inventories), 2},
		{"survey specs", len(snap.Surveys), 1},
		{"credentials", len(snap.Credentials), 2},
	} {
		if c.got < c.min {
			t.Fatalf("golden fixture breadth: %d %s, want >= %d (ADR-0089 D6 property 2). "+
				"A narrowed Enumerate makes this golden a happy-path subset, and the file-by-file "+
				"drift comparison below CANNOT catch that — regenerating with -update would make "+
				"the smaller emission self-consistent and green.", c.got, c.what, c.min)
		}
	}
}
