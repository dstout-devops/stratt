package desiredstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goldenBundle is the awx plugin's committed emitter-generated adopt bundle (ADR-0089 §6). It
// lives in the plugin (which owns the AWX→CaC transform); this core test reaches across the
// module boundary by PATH (never an import — core must not depend on the plugin) to prove the
// plugin↔core CaC contract: everything the plugin emits parses + validates through the core
// desiredstate reader. Paired with the plugin's TestGoldenBundle (which fails on emit drift),
// this is the §1.5 guarantee that plugin emission and core consumption never silently diverge.
// Regenerate the fixture with: go test ./plugins/awx/materialize -run TestGoldenBundle -update
const goldenBundle = "../../../plugins/awx/materialize/testdata/golden"

// TestAdoptGoldenBundleParses is the CaC contract test: the plugin-emitted golden bundle must
// parse + validate through desiredstate.ParseDir (KnownFields(true) + Validate*), and yield the
// full set of Named Kinds the awxsim estate covers. If the plugin's emit yaml tags or required
// fields drift from what core can consume, this fails loudly at the boundary (§1.5).
func TestAdoptGoldenBundleParses(t *testing.T) {
	decls, err := ParseDir(filepath.Clean(goldenBundle), nil)
	if err != nil {
		t.Fatalf("plugin-emitted adopt bundle does not parse through the core CaC reader: %v", err)
	}
	if len(decls.Views) != 3 {
		t.Errorf("views: got %d want 3", len(decls.Views))
	}
	if len(decls.Workflows) != 3 { // 2 job templates + 1 workflow
		t.Errorf("workflows: got %d want 3", len(decls.Workflows))
	}
	if len(decls.CredentialRefs) != 2 {
		t.Errorf("credential refs: got %d want 2", len(decls.CredentialRefs))
	}
}

// TestAdoptStampedBundleParses closes the wrapper gap: production emission runs the golden
// (Bundle) through materialize.stampLineage, which prepends a `# adopted-from:` provenance
// COMMENT to every .yaml. This test proves that bannered form still parses through the core CaC
// reader — so the leading comment the plugin adds in production never breaks core consumption.
// (The plugin's TestInvoke covers the banner is PRESENT; this covers it stays PARSEABLE.)
func TestAdoptStampedBundleParses(t *testing.T) {
	const banner = "# adopted-from: ctrl-a ctrl-a/10 (native ansible.template/10) at 2026-07-20T00:00:00Z (ADR-0086) — review before merge.\n"
	src := filepath.Clean(goldenBundle)
	dst := t.TempDir()
	err := filepath.Walk(src, func(path string, info os.FileInfo, werr error) error {
		if werr != nil || info.IsDir() {
			return werr
		}
		rel, _ := filepath.Rel(src, path)
		content, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.HasSuffix(rel, ".yaml") {
			content = append([]byte(banner), content...) // mirror stampLineage
		}
		full := filepath.Join(dst, rel)
		if merr := os.MkdirAll(filepath.Dir(full), 0o755); merr != nil {
			return merr
		}
		return os.WriteFile(full, content, 0o644)
	})
	if err != nil {
		t.Fatalf("stage bannered bundle: %v", err)
	}
	if _, err := ParseDir(dst, nil); err != nil {
		t.Fatalf("stamped (adopted-from bannered) bundle does not parse through the core CaC reader: %v", err)
	}
}
