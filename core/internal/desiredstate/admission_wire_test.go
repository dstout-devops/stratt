package desiredstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/policy"
)

// admissionEstate builds a temp estate carrying the SHIPPED admission policy plus
// one subject declaration, so the wiring is tested against the real controls.
func admissionEstate(t *testing.T, subdir, fname, content string) string {
	t.Helper()
	root := t.TempDir()
	pol, err := os.ReadFile("../../../estate/admission/baseline.yaml")
	if err != nil {
		t.Fatalf("read shipped admission policy: %v", err)
	}
	write := func(dir, name string, data []byte) {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("admission", "baseline.yaml", pol)
	write(subdir, fname, []byte(content))
	if err := os.MkdirAll(filepath.Join(root, "views"), 0o755); err != nil { // views/ is non-optional
		t.Fatal(err)
	}
	return root
}

func TestAdmission_DeniesForbiddenWorkflow(t *testing.T) {
	root := admissionEstate(t, "workflows", "forbidden-thing.yaml",
		"name: forbidden-thing\nsteps:\n  - name: s\n    viewName: v\n    actuator: script\n    params: {script: hi}\n")
	if _, err := ParseDir(root, policy.CEL{}); err == nil || !strings.Contains(err.Error(), "admission denied") {
		t.Fatalf("a forbidden-named workflow must be denied at admission, got %v", err)
	}
}

func TestAdmission_DeniesExportableCert(t *testing.T) {
	root := admissionEstate(t, "intents", "cert.yaml",
		"name: my-cert\nkind: Intent/Certificate\nspec:\n  exportable: true\n")
	if _, err := ParseDir(root, policy.CEL{}); err == nil || !strings.Contains(err.Error(), "admission denied") {
		t.Fatalf("an exportable cert Intent must be denied at admission, got %v", err)
	}
}

func TestAdmission_AllowsCleanDeclaration(t *testing.T) {
	root := admissionEstate(t, "workflows", "ok-thing.yaml",
		"name: ok-thing\nsteps:\n  - name: s\n    viewName: v\n    actuator: script\n    params: {script: hi}\n")
	if _, err := ParseDir(root, policy.CEL{}); err != nil {
		t.Fatalf("a clean declaration must pass admission, got %v", err)
	}
}

// A nil decider skips admission (a boot-time authz-only load), so even a
// would-be-denied declaration parses.
func TestAdmission_SkippedWithoutDecider(t *testing.T) {
	root := admissionEstate(t, "workflows", "forbidden-thing.yaml",
		"name: forbidden-thing\nsteps:\n  - name: s\n    viewName: v\n    actuator: script\n    params: {script: hi}\n")
	if _, err := ParseDir(root, nil); err != nil {
		t.Fatalf("nil decider must skip admission, got %v", err)
	}
}

// The shipped estate admission policy itself must validate (CI guard).
func TestEstateAdmissionPolicyValidates(t *testing.T) {
	raw, err := os.ReadFile("../../../estate/admission/baseline.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseAdmissionFile("baseline.yaml", raw); err != nil {
		t.Fatalf("shipped admission policy must validate at load: %v", err)
	}
}
