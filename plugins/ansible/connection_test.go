package ansible

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// oneKey / twoKeys / noMount are credential-mount listers standing in for the pod's
// /runner/credentials — the same injection point vaultPasswordFile's tests use.
func oneKey(string) ([]string, error)  { return []string{"id_ed25519"}, nil }
func twoKeys(string) ([]string, error) { return []string{"id_rsa", "id_ed25519"}, nil }
func noMount(string) ([]string, error) { return nil, errors.New("no such directory") }

// fakeStage stands in for the 0600 staging copy, returning a predictable path.
func fakeStage(mounted string) (string, error) {
	return "/runner/staged/" + filepath.Base(mounted), nil
}

// The decision in one assertion (ADR-0126 D1): the private-key path is DERIVED from the
// CredentialRef the Step is authorized to use, not written by hand. Before this, every
// Workflow carried `ansible_ssh_private_key_file: /tmp/<name>-key` in extraVars — so the
// authorized credential and the file actually read were two facts nothing reconciled
// (§2.4), and ADR-0084 D4 claimed a mechanism that did not exist.
func TestConnectionKeyComesFromTheCredentialMount(t *testing.T) {
	vars, err := connectionVars(&connectionParams{
		User: "appops", CredentialRef: "app-node-key",
	}, nil, "/runner/known_hosts", oneKey, fakeStage)
	if err != nil {
		t.Fatal(err)
	}
	if got := vars["ansible_ssh_private_key_file"]; got != "/runner/staged/id_ed25519" {
		t.Errorf("the key path must be the STAGED copy of the ref's mount, got %q", got)
	}
	if vars["ansible_user"] != "appops" {
		t.Errorf("ansible_user must be rendered by the shim: %q", vars["ansible_user"])
	}
}

// A ref that is not mounted means it is not on the Step's credentialRefs — the single
// authorization path (§2.5). The failure has to say that, because "file not found" sends
// an operator looking in the wrong place entirely (§1.8). Inherited verbatim from
// vaultPasswordFile by sharing credentialFile rather than copying it.
func TestUnmountedConnectionRefNamesTheFix(t *testing.T) {
	_, err := connectionVars(&connectionParams{CredentialRef: "missing"}, nil, "", noMount, fakeStage)
	if err == nil || !strings.Contains(err.Error(), "is it on the Step's credentialRefs?") {
		t.Fatalf("an unmounted ref must name the fix, got %v", err)
	}
	if !strings.Contains(err.Error(), "connection credentialRef") {
		t.Errorf("the failure must name WHICH credential (connection, not vault): %v", err)
	}
}

// An ambiguous mount is diagnosed rather than guessed — picking one would work until the
// day it silently picked the wrong key.
func TestAmbiguousConnectionMountIsDiagnosed(t *testing.T) {
	_, err := connectionVars(&connectionParams{CredentialRef: "multi"}, nil, "", twoKeys, fakeStage)
	if err == nil || !strings.Contains(err.Error(), "params.connection.file") {
		t.Fatalf("an ambiguous mount must name the knob that resolves it, got %v", err)
	}
	// And the vault caller must still name ITS own knob — the shared helper must not
	// have collapsed two diagnoses into one wrong one.
	_, verr := vaultPasswordFile(&vaultParams{CredentialRef: "multi"}, twoKeys)
	if verr == nil || !strings.Contains(verr.Error(), "params.vault.file") {
		t.Fatalf("vault must still name params.vault.file, got %v", verr)
	}
}

// ADR-0126 D2: the default VERIFIES. The estate used to carry
// `-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null` copy-pasted into every
// Workflow, which does not merely disable the check — with a /dev/null known-hosts it
// makes every connection a fresh trust-on-first-use, so nothing is ever detected.
func TestHostKeyCheckingDefaultsToVerifying(t *testing.T) {
	vars, err := connectionVars(&connectionParams{}, nil, "/runner/known_hosts", oneKey, fakeStage)
	if err != nil {
		t.Fatal(err)
	}
	args := vars["ansible_ssh_common_args"]
	if !strings.Contains(args, "StrictHostKeyChecking=accept-new") {
		t.Errorf("the DEFAULT must verify, got %q", args)
	}
	// accept-new is worth exactly as much as the file it remembers into.
	if !strings.Contains(args, "UserKnownHostsFile=/runner/known_hosts") {
		t.Errorf("accept-new needs somewhere to remember the key, got %q", args)
	}
	if strings.Contains(args, "/dev/null") {
		t.Errorf("the /dev/null known-hosts is the thing being removed: %q", args)
	}
}

// `off` still exists — some estates need it — but it now has to be WRITTEN DOWN, which
// is the whole change: a reviewer sees the word, not an argument buried in a flag string.
func TestHostKeyCheckingOffIsExplicit(t *testing.T) {
	vars, err := connectionVars(&connectionParams{HostKeyChecking: HostKeyOff}, nil, "/runner/known_hosts", oneKey, fakeStage)
	if err != nil {
		t.Fatal(err)
	}
	args := vars["ansible_ssh_common_args"]
	if !strings.Contains(args, "StrictHostKeyChecking=no") {
		t.Errorf("off must disable the check: %q", args)
	}
	// No known-hosts when the check is off: passing one would imply a memory that
	// nothing consults, which is the kind of decorative security this ADR removes.
	if strings.Contains(args, "UserKnownHostsFile") {
		t.Errorf("off must not pretend to remember anything: %q", args)
	}
	if _, err := connectionVars(&connectionParams{HostKeyChecking: "maybe"}, nil, "", oneKey, fakeStage); err == nil {
		t.Error("an unknown policy must be refused, not silently defaulted")
	}
}

// A target that needs no credential (a local-connection Entity) must still run — the
// connection block is legitimately absent, and a nil-hostile shim would break every
// gather-facts Run against a control-node target.
func TestNoConnectionBlockStillRuns(t *testing.T) {
	vars, err := connectionVars(nil, nil, "/runner/known_hosts", noMount, fakeStage)
	if err != nil {
		t.Fatalf("an absent connection block is legitimate: %v", err)
	}
	if _, ok := vars["ansible_ssh_private_key_file"]; ok {
		t.Error("no credential declared ⇒ no key var")
	}
}

// The group vars reach the inventory, sorted — byte-stability is what makes two Runs
// comparable during descent (§1.8), the same property buildInventory already holds for
// targets.
func TestRenderInventoryAppendsSortedGroupVars(t *testing.T) {
	inv := renderInventory(
		[]Target{{Name: "web1", Address: "10.0.0.1"}},
		map[string]string{"ansible_user": "appops", "ansible_ssh_common_args": "-o X=1"},
	)
	if !strings.Contains(inv, "web1") || !strings.Contains(inv, "ansible_host=10.0.0.1") {
		t.Fatalf("target rendering must be untouched:\n%s", inv)
	}
	body, vars, found := strings.Cut(inv, "[all:vars]")
	if !found {
		t.Fatalf("group vars must land in [all:vars]:\n%s", inv)
	}
	if strings.Contains(body, "ansible_user") {
		t.Error("group vars must not leak into the host lines")
	}
	if strings.Index(vars, "ansible_ssh_common_args") > strings.Index(vars, "ansible_user") {
		t.Errorf("group vars must be sorted for byte-stability:\n%s", vars)
	}
	// No connection ⇒ byte-identical to the pre-v6 inventory.
	if got := renderInventory([]Target{{Name: "web1", Address: "10.0.0.1"}}, nil); strings.Contains(got, "[all:vars]") {
		t.Errorf("an empty connection must not add a section:\n%s", got)
	}
}
