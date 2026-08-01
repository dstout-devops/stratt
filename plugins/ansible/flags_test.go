package ansible

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// noDir is a vault lister that must never be consulted (no vault param in play).
func noDir(t *testing.T) func(string) ([]string, error) {
	return func(dir string) ([]string, error) {
		t.Fatalf("vault lister consulted for %q with no vault param", dir)
		return nil, nil
	}
}

// TestPlaybookFlags_TypedKnobs pins that every v5 run knob (ADR-0117 D1) renders as
// its OWN token from the typed value — the property that makes this an argument
// surface rather than an injection one.
func TestPlaybookFlags_TypedKnobs(t *testing.T) {
	p := params{
		Become:    &becomeParams{Enabled: true, User: "deploy", Method: "sudo"},
		Limit:     "web-01",
		Tags:      []string{"install", "config"},
		SkipTags:  []string{"slow"},
		Forks:     10,
		Timeout:   30,
		Verbosity: 3,
	}
	got, err := playbookFlags(p, false, noDir(t))
	if err != nil {
		t.Fatalf("playbookFlags: %v", err)
	}
	want := []string{
		"--become", "--become-user", "deploy", "--become-method", "sudo",
		"--limit", "web-01",
		"--tags", "install,config",
		"--skip-tags", "slow",
		"--forks", "10",
		"--timeout", "30",
		"-vvv",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("flags:\n got %q\nwant %q", got, want)
	}
	// Each value is its own token — never fused into a neighbour.
	for _, tok := range got {
		if strings.Contains(tok, " ") {
			t.Fatalf("token %q contains a space — values must never be concatenated", tok)
		}
	}
}

// TestPlaybookFlags_NoKnobsNoCmdline: a Step with no knobs and no dry-run produces NO
// flags at all, so the shim appends no --cmdline (an empty one would change argv).
func TestPlaybookFlags_NoKnobsNoCmdline(t *testing.T) {
	got, err := playbookFlags(params{Play: "- hosts: all"}, false, noDir(t))
	if err != nil {
		t.Fatalf("playbookFlags: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no flags, got %q", got)
	}
}

// TestPlaybookFlags_CheckModeIsThePortBit: DryRun (the port bit, ADR-0051 MF6) always
// yields --check --diff and always wins; `diff` alone applies-and-shows. The two are
// distinct requests and must not double up.
func TestPlaybookFlags_CheckModeIsThePortBit(t *testing.T) {
	dry, err := playbookFlags(params{Diff: true}, true, noDir(t))
	if err != nil {
		t.Fatalf("playbookFlags: %v", err)
	}
	if strings.Join(dry, " ") != "--check --diff" {
		t.Fatalf("dry-run flags = %q, want exactly --check --diff (no duplicate --diff)", dry)
	}

	apply, err := playbookFlags(params{Diff: true}, false, noDir(t))
	if err != nil {
		t.Fatalf("playbookFlags: %v", err)
	}
	if strings.Join(apply, " ") != "--diff" {
		t.Fatalf("apply+diff flags = %q, want --diff (converge and show)", apply)
	}

	// A deprecated params.check is NOT read: it is absent from the shim's params, so
	// it can never turn an applying run into a check run (ADR-0117 D2).
	plain, err := playbookFlags(params{}, false, noDir(t))
	if err != nil {
		t.Fatalf("playbookFlags: %v", err)
	}
	if len(plain) != 0 {
		t.Fatalf("apply run with no knobs must produce no flags, got %q", plain)
	}
}

// TestVaultPasswordFile covers the three resolutions and their diagnoses (§1.8): the
// single injected file is inferred, an explicit name is honored, and ambiguity or a
// missing mount FAILS with an actionable message rather than guessing.
func TestVaultPasswordFile(t *testing.T) {
	one := func(string) ([]string, error) { return []string{"vault-pw"}, nil }
	if got, err := vaultPasswordFile(&vaultParams{CredentialRef: "vp"}, one); err != nil ||
		got != "/runner/credentials/vp/vault-pw" {
		t.Fatalf("single-file infer = %q err=%v", got, err)
	}

	many := func(string) ([]string, error) { return []string{"a", "b"}, nil }
	if got, err := vaultPasswordFile(&vaultParams{CredentialRef: "vp", File: "b"}, many); err != nil ||
		got != "/runner/credentials/vp/b" {
		t.Fatalf("explicit file = %q err=%v", got, err)
	}

	_, err := vaultPasswordFile(&vaultParams{CredentialRef: "vp"}, many)
	if err == nil || !strings.Contains(err.Error(), "params.vault.file") {
		t.Fatalf("ambiguous mount must name the fix, got %v", err)
	}

	missing := func(string) ([]string, error) { return nil, errors.New("no such directory") }
	_, err = vaultPasswordFile(&vaultParams{CredentialRef: "vp"}, missing)
	if err == nil || !strings.Contains(err.Error(), "credentialRefs") {
		t.Fatalf("unmounted ref must point at the Step's credentialRefs, got %v", err)
	}
}

// vaultRaw marshals a vault declaration into params.Vault's raw form, so a test states
// the SHAPE an estate writes rather than a Go struct that hides which of the two v8
// accepts (ADR-0153 D4).
func vaultRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestPlaybookFlags_VaultRendersPasswordFile: a vault ref becomes
// --vault-password-file <resolved path>, and a resolution failure is returned (the
// caller turns it into a terminal fatal) rather than silently dropping the flag.
func TestPlaybookFlags_VaultRendersPasswordFile(t *testing.T) {
	one := func(string) ([]string, error) { return []string{"pw.txt"}, nil }
	got, err := playbookFlags(params{Vault: vaultRaw(t, vaultParams{CredentialRef: "vault-dev"})}, false, one)
	if err != nil {
		t.Fatalf("playbookFlags: %v", err)
	}
	if strings.Join(got, " ") != "--vault-password-file /runner/credentials/vault-dev/pw.txt" {
		t.Fatalf("vault flags = %q", got)
	}

	boom := func(string) ([]string, error) { return nil, errors.New("nope") }
	if _, err := playbookFlags(params{Vault: vaultRaw(t, vaultParams{CredentialRef: "x"})}, false, boom); err == nil {
		t.Fatal("a vault resolution failure must surface, not be dropped")
	}
}
