package awximport

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/connectors/awx"
	"github.com/dstout-devops/stratt/core/internal/connectors/awx/awxsim"
	"github.com/dstout-devops/stratt/core/internal/desiredstate"
)

// snapshotFromSim enumerates the canned awxsim estate through the real client.
func snapshotFromSim(t *testing.T) *awx.Snapshot {
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
	return snap
}

// TestBundleRoundTrips is the load-bearing test: every emitted declaration
// parses and validates through desiredstate.ParseDir (KnownFields(true) +
// Validate*). If a yaml tag or a required field drifts, this fails.
func TestBundleRoundTrips(t *testing.T) {
	snap := snapshotFromSim(t)
	emit, err := Bundle(snap, Options{})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// t.TempDir is non-empty checks: write into a fresh subdir.
	bundle := filepath.Join(dir, "out")
	if err := WriteBundle(bundle, emit); err != nil {
		t.Fatal(err)
	}

	decls, err := desiredstate.ParseDir(bundle)
	if err != nil {
		t.Fatalf("emitted bundle does not round-trip through ParseDir: %v", err)
	}
	if len(decls.Views) != 3 {
		t.Fatalf("views: got %d want 3", len(decls.Views))
	}
	if len(decls.Workflows) != 3 { // 2 job templates + 1 workflow
		t.Fatalf("workflows: got %d want 3", len(decls.Workflows))
	}
	if len(decls.CredentialRefs) != 2 {
		t.Fatalf("credential refs: got %d want 2", len(decls.CredentialRefs))
	}
	if _, err := os.Stat(filepath.Join(bundle, "migration-report.md")); err != nil {
		t.Fatalf("migration report missing: %v", err)
	}
}

// TestReportUsesStrattVocabulary guards §2: no banned core-model identifier
// leaks into an emitted YAML declaration key. AWX nouns are allowed only in the
// report prose and in awx.* provenance labels/names.
func TestNoBannedVocabularyInDeclarations(t *testing.T) {
	snap := snapshotFromSim(t)
	emit, err := Bundle(snap, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Banned Stratt core-model identifiers. Note: `playbook` inside ansible
	// params is tool-native (the ansible Contract's own field) and allowed
	// (§2); the guard targets AWX object nouns that must never become Stratt
	// identifiers.
	banned := []string{"job_template", "jobtemplate", "job template", "cmdb"}
	for path, doc := range emit.Files {
		if strings.HasSuffix(path, ".md") {
			continue
		}
		low := strings.ToLower(doc)
		for _, b := range banned {
			if strings.Contains(low, b) {
				t.Errorf("%s contains banned token %q:\n%s", path, b, doc)
			}
		}
	}
}
