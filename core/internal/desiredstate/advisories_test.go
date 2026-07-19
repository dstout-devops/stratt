package desiredstate

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadSoftwareAdvisories proves ADR-0080 slice 2: the advisory ruleset loads
// from estate Git (compliance-as-data), and a malformed/incomplete ruleset fails
// LOUD (§1.8) rather than silently disabling patch Findings.
func TestLoadSoftwareAdvisories(t *testing.T) {
	t.Run("absent dir is a no-op", func(t *testing.T) {
		got, err := LoadSoftwareAdvisories(t.TempDir())
		if err != nil || got != nil {
			t.Fatalf("absent advisories/ must be empty no-op, got %v, %v", got, err)
		}
	})

	t.Run("valid ruleset loads", func(t *testing.T) {
		root := t.TempDir()
		writeAdvisory(t, root, "a.yaml", `advisories:
  - id: CVE-2022-3602
    component: openssl
    fixed: "3.0.7"
    severity: high
  - id: EOL-1
    component: openssl
    affected: ["1.1.1"]
    severity: critical`)
		got, err := LoadSoftwareAdvisories(root)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if len(got) != 2 || got[0].Component != "openssl" || got[0].Fixed != "3.0.7" || len(got[1].Affected) != 1 {
			t.Fatalf("parsed advisories wrong: %+v", got)
		}
	})

	t.Run("missing id/component fails loud", func(t *testing.T) {
		root := t.TempDir()
		writeAdvisory(t, root, "bad.yaml", `advisories:
  - component: openssl
    fixed: "3.0.7"`)
		if _, err := LoadSoftwareAdvisories(root); err == nil {
			t.Fatal("an advisory with no id must be a loud error")
		}
	})

	t.Run("advisory matching nothing fails loud", func(t *testing.T) {
		root := t.TempDir()
		writeAdvisory(t, root, "empty.yaml", `advisories:
  - id: CVE-X
    component: openssl`)
		if _, err := LoadSoftwareAdvisories(root); err == nil {
			t.Fatal("an advisory with neither fixed nor affected must be a loud error")
		}
	})
}

func writeAdvisory(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, "advisories")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
