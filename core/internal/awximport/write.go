package awximport

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// WriteBundle writes an Emit to dir: the declaration files under their kind
// subdirectories plus migration-report.md. This is the only filesystem write in
// the importer — the CLI calls it after Bundle. dir is created if absent; an
// existing non-empty dir is an error (a migration bundle is written fresh).
func WriteBundle(dir string, e *Emit) error {
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return fmt.Errorf("awximport: output dir %q is not empty; choose a fresh directory", dir)
	}
	paths := make([]string, 0, len(e.Files))
	for p := range e.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, rel := range paths {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("awximport: %w", err)
		}
		if err := os.WriteFile(full, []byte(e.Files[rel]), 0o644); err != nil {
			return fmt.Errorf("awximport: %w", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "migration-report.md"), []byte(e.Report), 0o644); err != nil {
		return fmt.Errorf("awximport: %w", err)
	}
	return nil
}
