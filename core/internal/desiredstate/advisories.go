package desiredstate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/dstout-devops/stratt/types"
)

// advisoriesFile is the estate shape of a software-advisory ruleset (ADR-0080):
// a list of advisories under advisories/. Compliance-as-data — WHO decides
// "vulnerable" is declared in Git and loaded on the reconcile cadence, never
// hardcoded (§1.2).
type advisoriesFile struct {
	Advisories []types.SoftwareAdvisory `yaml:"advisories"`
}

// LoadSoftwareAdvisories reads every advisories/*.yaml under root and returns the
// merged software-advisory ruleset the check consumes (ADR-0080 slice 2) — over the
// whole software dimension (packages, container images, charts). An absent
// advisories/ dir is a no-op (empty ruleset). A malformed or incomplete advisory
// file is a LOUD error (§1.8): a broken ruleset must never silently disable
// patch/vulnerability Findings.
func LoadSoftwareAdvisories(root string) ([]types.SoftwareAdvisory, error) {
	dir := filepath.Join(root, "advisories")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("desiredstate: advisories read: %w", err)
	}
	var out []types.SoftwareAdvisory
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("desiredstate: advisories %s: %w", e.Name(), err)
		}
		var f advisoriesFile
		if err := yaml.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("desiredstate: advisories %s: %w", e.Name(), err)
		}
		for i, a := range f.Advisories {
			if a.ID == "" || a.Component == "" {
				return nil, fmt.Errorf("desiredstate: advisories %s: entry %d requires both `id` and `component`", e.Name(), i)
			}
			if a.Fixed == "" && len(a.Affected) == 0 {
				return nil, fmt.Errorf("desiredstate: advisories %s: advisory %q needs `fixed` or `affected` (else it matches nothing)", e.Name(), a.ID)
			}
		}
		out = append(out, f.Advisories...)
	}
	return out, nil
}
