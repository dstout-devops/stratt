package desiredstate

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestDemoFidelityMatchesManifest refuses a demo whose declared `fidelity` disagrees
// with the grade the demo library's table advertises.
//
// The honesty claim is recorded in TWO places — `demo.yaml`, which the turnkey runner
// prints, and the table in `demos/README.md`, which is what a reader meets first —
// and this test exists because they had already drifted: the table graded ec2-only
// `real` while its manifest said `build-real`, months after floci was measured and
// found not to be SSH-able. The prose in that row had been corrected; the one word a
// reader actually scans had not.
//
// That is the worst possible failure of this kind. A stale red is a to-do; a stale
// green is a lie, and fidelity is the single claim the demo library exists to keep
// honest (ADR-0116 D3; charter §1.8 — hide mechanism, never fidelity).
func TestDemoFidelityMatchesManifest(t *testing.T) {
	readmePath := filepath.Join("..", "..", "..", "demos", "README.md")
	raw, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatal(err)
	}
	rows, col := demoTableRows(t, string(raw))

	link := regexp.MustCompile(`\]\(([^)]+)\)`)
	graded := map[string]bool{}
	for _, cells := range rows {
		m := link.FindStringSubmatch(cells[0])
		if m == nil {
			t.Errorf("demos/README.md library row %q has no link, so its demo cannot be identified", strings.TrimSpace(cells[0]))
			continue
		}
		// The link is relative to demos/README.md and points at the demo's own README;
		// its directory is where the manifest lives.
		manifest := filepath.Join(filepath.Dir(readmePath), path.Dir(m[1]), "demo.yaml")
		declared := manifestFidelity(t, manifest)
		if declared == "" {
			continue
		}
		graded[filepath.Clean(manifest)] = true

		advertised := strings.Trim(strings.TrimSpace(cells[col]), "`")
		if advertised != declared {
			t.Errorf("%s declares fidelity %q but demos/README.md advertises %q — "+
				"the grade a reader scans must never outrank the one the runner prints",
				manifest, declared, advertised)
		}
	}

	// Coverage, so the check cannot pass by matching nothing: every demo manifest in
	// the repo must appear in the table. An UNLISTED demo is the same defect wearing a
	// different hat — a fidelity claim nobody sees.
	manifests, err := filepath.Glob(filepath.Join("..", "..", "..", "plugins", "*", "demo", "demo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	more, err := filepath.Glob(filepath.Join("..", "..", "..", "demos", "*", "demo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range append(manifests, more...) {
		if !graded[filepath.Clean(m)] {
			t.Errorf("%s is a demo the library table does not list, so its fidelity claim is unreviewed", m)
		}
	}
	if len(graded) == 0 {
		t.Fatal("no demo rows matched a manifest — the table's shape changed and this guard is checking nothing")
	}
}

// demoTableRows returns the library table's data rows (split into cells) and the
// index of the Fidelity column, located by HEADER NAME rather than position so a
// reordered or widened table fails loudly instead of comparing the wrong column.
func demoTableRows(t *testing.T, md string) ([][]string, int) {
	t.Helper()
	col := -1
	var rows [][]string
	for _, line := range strings.Split(md, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		switch {
		case col < 0:
			for i, c := range cells {
				if strings.EqualFold(strings.TrimSpace(c), "fidelity") {
					col = i
				}
			}
		case strings.HasPrefix(strings.TrimSpace(cells[0]), "---"):
			// separator
		case col < len(cells):
			rows = append(rows, cells)
		}
	}
	if col < 0 {
		t.Fatal("demos/README.md has no Fidelity column — the library table's shape changed")
	}
	return rows, col
}

func manifestFidelity(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		// Not every table row has to be a demo directory in this repo, but a broken
		// link is worth reporting rather than skipping in silence.
		t.Errorf("demos/README.md links to %s, which has no demo.yaml", path)
		return ""
	}
	var doc struct {
		Fidelity string `yaml:"fidelity"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Errorf("parse %s: %v", path, err)
		return ""
	}
	if doc.Fidelity == "" {
		t.Errorf("%s declares no fidelity (ADR-0116 D3 requires one)", path)
	}
	return doc.Fidelity
}
