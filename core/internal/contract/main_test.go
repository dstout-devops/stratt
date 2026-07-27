package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain registers every plugin's SELF contracts before the package's tests run.
//
// Since ADR-0138 D3/D4 those documents live in `plugins/<n>/contracts/` and reach the registry
// through the estate parse rather than the binary's embedded FS. This package cannot call that
// parse — desiredstate imports contract, so the dependency only runs one way — so it reads the
// trees directly, which is also the more honest test: it exercises RegisterEstate itself, the
// mechanism the parse relies on.
func TestMain(m *testing.M) {
	root := "../../../plugins"
	entries, err := os.ReadDir(root)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(root, e.Name(), "contracts")
			docs := map[string][]byte{}
			_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, werr error) error {
				if werr != nil || d.IsDir() || !strings.HasSuffix(path, ".schema.json") {
					return nil
				}
				rel, rerr := filepath.Rel(dir, path)
				if rerr != nil {
					return nil
				}
				raw, rerr := os.ReadFile(path)
				if rerr != nil {
					return nil
				}
				docs[strings.TrimSuffix(filepath.ToSlash(rel), ".schema.json")] = raw
				return nil
			})
			if len(docs) > 0 {
				if err := RegisterEstate(e.Name(), docs); err != nil {
					println("contract tests: register", e.Name(), ":", err.Error())
				}
			}
		}
	}
	os.Exit(m.Run())
}
