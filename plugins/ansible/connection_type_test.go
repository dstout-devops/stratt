package ansible

import (
	"encoding/json"
	"strings"
	"testing"
)

// ── ADR-0153 · the reach gap ────────────────────────────────────────────────────────────

// network_cli is the value this whole ADR exists for: the ansible.netcommon family is a
// large part of why enterprises buy AAP, and before v8 the Actuator could reach Linux over
// SSH-with-a-key and nothing else.
func TestNetworkCLIRendersConnectionAndNetworkOS(t *testing.T) {
	vars, err := connectionVars(&connectionParams{
		Type: ConnNetworkCLI, NetworkOS: "cisco.ios.ios", User: "netops",
	}, nil, "", false, noMount, fakeStage)
	if err != nil {
		t.Fatalf("connectionVars: %v", err)
	}
	if vars["ansible_connection"] != "network_cli" || vars["ansible_network_os"] != "cisco.ios.ios" {
		t.Fatalf("netcommon vars = %v", vars)
	}
	if vars["ansible_user"] != "netops" {
		t.Errorf("the user is orthogonal to the connection type and must still render: %v", vars)
	}
}

// An ssh declaration must render EXACTLY as it did before v8 existed. The registry keeps
// one live actuators/ansible.input and a Step cannot pin a version (ADR-0132 D4), so a v8
// that changed the default rendering would change every shipped Step's behaviour silently.
func TestDefaultTypeChangesNothing(t *testing.T) {
	before, err := connectionVars(&connectionParams{User: "appops"}, nil, "", false, noMount, fakeStage)
	if err != nil {
		t.Fatal(err)
	}
	after, err := connectionVars(&connectionParams{Type: ConnSSH, User: "appops"}, nil, "", false, noMount, fakeStage)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := before["ansible_connection"]; present {
		t.Errorf("an ssh run must author NO ansible_connection — ansible's own default — so an "+
			"upgrade to v8 changes no rendered inventory: %v", before)
	}
	if len(before) != len(after) {
		t.Errorf("explicit ssh and default ssh must render identically: %v vs %v", before, after)
	}
}

// winrm is the single most-asked-for row in the register and the answer is still NO. It
// must fail HERE — at the Contract and at the shim — rather than be accepted and then
// discover at 3 a.m. that nothing ever ran that path.
func TestWindowsIsRefusedByName(t *testing.T) {
	for _, typ := range []string{"winrm", "psrp", "docker", "kubectl", "httpapi"} {
		_, err := connectionVars(&connectionParams{Type: typ}, nil, "", false, noMount, fakeStage)
		if err == nil {
			t.Fatalf("connection.type %q was accepted — an enum that admits a value the shim has "+
				"never honored fails on a migrated fleet instead of at estate load", typ)
		}
		if !strings.Contains(err.Error(), typ) {
			t.Errorf("the refusal must name the offending value, got %v", err)
		}
	}
}

// Required, not defaulted. A guessed vendor CONNECTS and then speaks another vendor's
// syntax, so the failure surfaces inside the play rather than at the connection.
func TestNetworkOSIsRequiredAndIsRefusedOnSSH(t *testing.T) {
	_, err := connectionVars(&connectionParams{Type: ConnNetworkCLI}, nil, "", false, noMount, fakeStage)
	if err == nil || !strings.Contains(err.Error(), "networkOS") {
		t.Fatalf("network_cli without networkOS must fail and say which field, got %v", err)
	}
	if !strings.Contains(err.Error(), "cisco.ios.ios") {
		t.Errorf("§1.8 — the diagnosis should show the shape of the answer: %v", err)
	}

	_, err = connectionVars(&connectionParams{NetworkOS: "cisco.ios.ios"}, nil, "", false, noMount, fakeStage)
	if err == nil {
		t.Fatal("ansible_network_os means nothing to the ssh plugin — accepting it would render a " +
			"var nothing reads and let an operator believe a device connection was configured")
	}
}

// THE PRECEDENCE REFUSAL (D6). `local` arrives as a HOST var, and in ansible host vars beat
// group vars — so a non-ssh type set here would be silently overridden for exactly those
// targets. Two declarations that each look correct, resolved by a rule nobody wrote.
func TestNonSSHTypeWithALocalTargetIsRefused(t *testing.T) {
	_, err := connectionVars(&connectionParams{Type: ConnNetworkCLI, NetworkOS: "frr.frr.frr"},
		nil, "", true, noMount, fakeStage)
	if err == nil {
		t.Fatal("a local target would keep connecting local while every other target went over " +
			"network_cli — one Run meaning two things, decided by ansible's var precedence")
	}
	for _, want := range []string{"network_cli", "local"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name both sides, missing %q: %v", want, err)
		}
	}
	// ssh + local is the ordinary case and must stay legal.
	if _, err := connectionVars(&connectionParams{User: "root"}, nil, "", true, noMount, fakeStage); err != nil {
		t.Errorf("a local target on an ssh run is exactly what mgmt.address's reserved value is for: %v", err)
	}
}

// `local` is a property of the TARGET. Accepting it here would be a second home for that
// fact (§2.4), so it is refused with that reason rather than with a generic enum message.
func TestLocalIsNotAParamsValue(t *testing.T) {
	_, err := connectionVars(&connectionParams{Type: "local"}, nil, "", false, noMount, fakeStage)
	if err == nil || !strings.Contains(err.Error(), "mgmt.address") {
		t.Fatalf("the refusal must point at where `local` actually belongs, got %v", err)
	}
}

// hasLocalTarget has to agree with buildInventory about what "local" means — two functions
// disagreeing is how the D6 refusal would silently stop firing.
func TestHasLocalTargetAgreesWithTheInventory(t *testing.T) {
	targets := []Target{{Name: "a", Address: "10.0.0.1"}, {Name: "b", Address: "local"}}
	if !hasLocalTarget(targets) {
		t.Fatal("a local target must be detected")
	}
	if !strings.Contains(buildInventory(targets), "ansible_connection=local") {
		t.Fatal("…and it must be the same target the inventory renders local")
	}
	if hasLocalTarget([]Target{{Name: "a", Address: "10.0.0.1"}, {Name: "c", Address: ""}}) {
		t.Error("an EMPTY address is unreachable-and-loud (§1.8), not local — treating it as local " +
			"is the silent-run-on-the-control-node failure buildInventory exists to prevent")
	}
}

// ── credential forms ────────────────────────────────────────────────────────────────────

// THE §2.5 PROPERTY, stated as a test: the password reaches ansible as a PATH. Rendering it
// as an inventory group var instead would put secret material in inventory/hosts, which
// writeInventory creates at 0644 beside ansible-runner's artifacts/.
func TestPasswordsAreFilePathsAndNeverValues(t *testing.T) {
	one := func(string) ([]string, error) { return []string{"pw"}, nil }
	got, err := playbookFlags(params{
		Connection: &connectionParams{PasswordRef: &passwordRef{CredentialRef: "device-pw"}},
		Become:     &becomeParams{Enabled: true, PasswordRef: &passwordRef{CredentialRef: "sudo-pw"}},
	}, false, one)
	if err != nil {
		t.Fatalf("playbookFlags: %v", err)
	}
	joined := strings.Join(got, " ")
	for _, want := range []string{
		"--connection-password-file /runner/credentials/device-pw/pw",
		"--become-password-file /runner/credentials/sudo-pw/pw",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %q", want, joined)
		}
	}

	// The connection vars must carry NO password of any kind: they are written into the
	// inventory, and the inventory is an artifact.
	vars, err := connectionVars(&connectionParams{
		PasswordRef: &passwordRef{CredentialRef: "device-pw"}, User: "netops",
	}, nil, "", false, one, fakeStage)
	if err != nil {
		t.Fatal(err)
	}
	for k := range vars {
		if strings.Contains(k, "password") || strings.Contains(k, "pass") {
			t.Errorf("connection var %q would be written to inventory/hosts at 0644 beside the "+
				"Run's artifacts — §2.5 forbids material there outright", k)
		}
	}
}

// An escalation password with no escalation requested: one of the two is a mistake, and
// guessing which yields either a pointless credential mount or a run that quietly does not
// escalate.
func TestBecomePasswordWithoutBecomeIsRefused(t *testing.T) {
	one := func(string) ([]string, error) { return []string{"pw"}, nil }
	_, err := playbookFlags(params{
		Become: &becomeParams{PasswordRef: &passwordRef{CredentialRef: "pw"}},
	}, false, one)
	if err == nil || !strings.Contains(err.Error(), "enabled") {
		t.Fatalf("must refuse and name the contradiction, got %v", err)
	}
}

// A password ref that cannot be resolved must SURFACE — the same rule the vault ref has
// carried since ADR-0117. Silently dropping the flag runs the play with no password and
// blames the target.
func TestUnresolvablePasswordRefSurfaces(t *testing.T) {
	two := func(string) ([]string, error) { return []string{"a", "b"}, nil }
	_, err := playbookFlags(params{
		Connection: &connectionParams{PasswordRef: &passwordRef{CredentialRef: "amb"}},
	}, false, two)
	if err == nil || !strings.Contains(err.Error(), "params.connection.passwordRef.file") {
		t.Fatalf("an ambiguous mount must name the knob that fixes it, got %v", err)
	}
}

// ── ANS-011 · multiple vault identities ─────────────────────────────────────────────────

func TestVaultAcceptsOneIdentityOrMany(t *testing.T) {
	one := func(string) ([]string, error) { return []string{"pw"}, nil }

	// The v7 OBJECT form. This is the compatibility promise: a Step cannot pin a version,
	// so an array-only v8 would fail every shipped declaration the moment it landed.
	got, err := playbookFlags(params{Vault: vaultRaw(t, vaultParams{CredentialRef: "dev"})}, false, one)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != "--vault-password-file /runner/credentials/dev/pw" {
		t.Fatalf("the v7 object form must render byte-identically, got %q", got)
	}

	// The v8 ARRAY form, mixed: an entry without an id keeps the v7 flag, one with an id
	// becomes --vault-id <id>@<path>.
	got, err = playbookFlags(params{Vault: vaultRaw(t, []vaultParams{
		{CredentialRef: "dev"},
		{CredentialRef: "prod", ID: "prod"},
		{CredentialRef: "legacy", ID: "legacy"},
	})}, false, one)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, " ")
	for _, want := range []string{
		"--vault-password-file /runner/credentials/dev/pw",
		"--vault-id prod@/runner/credentials/prod/pw",
		"--vault-id legacy@/runner/credentials/legacy/pw",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %q", want, joined)
		}
	}
}

// Two files claiming one identity is an ambiguity ansible resolves BY ORDER — a silent
// winner by another name (§2.4).
func TestDuplicateVaultIDIsRefused(t *testing.T) {
	one := func(string) ([]string, error) { return []string{"pw"}, nil }
	_, err := playbookFlags(params{Vault: vaultRaw(t, []vaultParams{
		{CredentialRef: "a", ID: "prod"}, {CredentialRef: "b", ID: "prod"},
	})}, false, one)
	if err == nil || !strings.Contains(err.Error(), "prod") {
		t.Fatalf("a duplicate vault id must be refused and named, got %v", err)
	}
}

// An unparseable vault block must fail loudly rather than resolve to "no vault", which
// would run the play against encrypted files and report a play error instead of a
// declaration error.
func TestMalformedVaultFails(t *testing.T) {
	one := func(string) ([]string, error) { return []string{"pw"}, nil }
	if _, err := playbookFlags(params{Vault: json.RawMessage(`"dev"`)}, false, one); err == nil {
		t.Fatal("a scalar vault must not silently mean no vault")
	}
	if got, err := playbookFlags(params{Vault: nil}, false, one); err != nil || len(got) != 0 {
		t.Fatalf("an absent vault stays absent: %q %v", got, err)
	}
}
