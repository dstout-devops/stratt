package materialize

import (
	"context"
	"flag"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	awx "github.com/dstout-devops/stratt/plugins/awx/controller"
	"github.com/dstout-devops/stratt/plugins/awx/controller/awxsim"
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
