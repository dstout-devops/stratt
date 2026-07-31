package ansible

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// ── ADR-0153 · the reach gap ────────────────────────────────────────────────────────────

// eeManifest fakes the EE's run-visible content manifest (/etc/stratt/ee-content.json).
func eeManifest(collections ...string) func(string) ([]byte, error) {
	entries := make([]string, 0, len(collections))
	for _, c := range collections {
		entries = append(entries, `{"name":"`+c+`","version":"1.0.0","declared":true}`)
	}
	doc := []byte(`{"collections":[` + strings.Join(entries, ",") + `],"roles":[]}`)
	return func(string) ([]byte, error) { return doc, nil }
}

// noEE is an ORDINARY Stratt EE: the platform floor and nothing else. It is the default in
// these tests because it is what an adopter actually runs — a netcommon-bearing image is
// the special case, and defaulting to it would have hidden the whole finding below.
var noEE = eeManifest("community.general")

// netEE is an EE variant built with the netcommon collection (ADR-0117 D3).
var netEE = eeManifest("community.general", "ansible.netcommon")

// network_cli is the value this whole ADR exists for: the ansible.netcommon family is a
// large part of why enterprises buy AAP, and before v8 the Actuator could reach Linux over
// SSH-with-a-key and nothing else.
func TestNetworkCLIRendersConnectionAndNetworkOS(t *testing.T) {
	vars, err := connectionVars(&connectionParams{
		Type: ConnNetworkCLI, NetworkOS: "cisco.ios.ios", User: "netops",
	}, nil, "", false, noMount, netEE, fakeStage)
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
	before, err := connectionVars(&connectionParams{User: "appops"}, nil, "", false, noMount, noEE, fakeStage)
	if err != nil {
		t.Fatal(err)
	}
	after, err := connectionVars(&connectionParams{Type: ConnSSH, User: "appops"}, nil, "", false, noMount, noEE, fakeStage)
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
		_, err := connectionVars(&connectionParams{Type: typ}, nil, "", false, noMount, noEE, fakeStage)
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
	_, err := connectionVars(&connectionParams{Type: ConnNetworkCLI}, nil, "", false, noMount, noEE, fakeStage)
	if err == nil || !strings.Contains(err.Error(), "networkOS") {
		t.Fatalf("network_cli without networkOS must fail and say which field, got %v", err)
	}
	if !strings.Contains(err.Error(), "cisco.ios.ios") {
		t.Errorf("§1.8 — the diagnosis should show the shape of the answer: %v", err)
	}

	_, err = connectionVars(&connectionParams{NetworkOS: "cisco.ios.ios"}, nil, "", false, noMount, noEE, fakeStage)
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
		nil, "", true, noMount, noEE, fakeStage)
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
	if _, err := connectionVars(&connectionParams{User: "root"}, nil, "", true, noMount, noEE, fakeStage); err != nil {
		t.Errorf("a local target on an ssh run is exactly what mgmt.address's reserved value is for: %v", err)
	}
}

// `local` is a property of the TARGET. Accepting it here would be a second home for that
// fact (§2.4), so it is refused with that reason rather than with a generic enum message.
func TestLocalIsNotAParamsValue(t *testing.T) {
	_, err := connectionVars(&connectionParams{Type: "local"}, nil, "", false, noMount, noEE, fakeStage)
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
	}, nil, "", false, one, noEE, fakeStage)
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

// ── the correction verification forced (ADR-0153, found after the ADR was written) ──────

// network_cli and netconf are NOT in ansible-core — measured, not assumed:
//
//	$ ansible-doc -t connection network_cli
//	[WARNING]: Error loading plugin 'ansible.netcommon.network_cli': No module named
//	           'ansible_collections.ansible.netcommon'
//
// So a Contract that accepts the value on an EE that cannot load the plugin passes review,
// passes the estate load, passes every test above — and dies at connect time naming a
// python module the estate never wrote. Closing only the enum would have been half a fix.
func TestNetcommonTypeIsRefusedOnAnEEThatCannotLoadIt(t *testing.T) {
	_, err := connectionVars(&connectionParams{Type: ConnNetworkCLI, NetworkOS: "cisco.ios.ios"},
		nil, "", false, noMount, noEE, fakeStage)
	if err == nil {
		t.Fatal("an ordinary EE ships the platform floor and NOT netcommon — accepting the type " +
			"here moves the failure from estate load to connect time, which is the exact defect " +
			"ADR-0153 D1 refuses one layer up")
	}
	for _, want := range []string{"ansible.netcommon", "network_cli", "community.general"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the diagnosis must name the missing collection, the type, and what IS "+
				"installed; missing %q: %v", want, err)
		}
	}
	if !strings.Contains(err.Error(), "ADR-0117") {
		t.Errorf("…and where the fix belongs — an EE variant, not the platform floor: %v", err)
	}

	// netconf needs the same collection and must be refused identically.
	if _, err := connectionVars(&connectionParams{Type: ConnNetconf, NetworkOS: "x.y.z"},
		nil, "", false, noMount, noEE, fakeStage); err == nil {
		t.Error("netconf lives in the same collection and must fail the same way")
	}
}

// ssh and local are ansible-core. The check must not fire for them — an image-capability
// failure on the connection type every existing estate uses would be a self-inflicted outage.
func TestSSHNeverConsultsTheEEManifest(t *testing.T) {
	exploded := func(string) ([]byte, error) {
		t.Helper()
		t.Fatal("an ssh run must not read the content manifest — ssh is ansible-core, there is " +
			"nothing to check, and a manifest problem must not break the ordinary path")
		return nil, nil
	}
	if _, err := connectionVars(&connectionParams{User: "appops"}, nil, "", false, noMount, exploded, fakeStage); err != nil {
		t.Fatalf("ssh must not be gated on image content: %v", err)
	}
}

// An unreadable manifest is neither "present" nor "missing". Guessing either way turns an
// image problem into a connection problem, and one of the two guesses is silently wrong.
func TestUnreadableManifestIsItsOwnDiagnosis(t *testing.T) {
	gone := func(string) ([]byte, error) { return nil, errors.New("no such file or directory") }
	_, err := connectionVars(&connectionParams{Type: ConnNetworkCLI, NetworkOS: "cisco.ios.ios"},
		nil, "", false, noMount, gone, fakeStage)
	if err == nil {
		t.Fatal("an image with no manifest was not built by our pipeline; what it contains is " +
			"unknown rather than adequate")
	}
	if !strings.Contains(err.Error(), "unknown rather than adequate") {
		t.Errorf("the diagnosis must distinguish unknown from missing: %v", err)
	}

	garbage := func(string) ([]byte, error) { return []byte("{not json"), nil }
	if _, err := connectionVars(&connectionParams{Type: ConnNetconf, NetworkOS: "x.y.z"},
		nil, "", false, noMount, garbage, fakeStage); err == nil {
		t.Error("a corrupt manifest must fail too, not parse to an empty collection set")
	}
}

// The DECLARATION is checked before the IMAGE, deliberately: an operator must never be sent
// to rebuild an EE over a typo they could have fixed in YAML.
func TestDeclarationErrorsAreReportedBeforeImageErrors(t *testing.T) {
	// networkOS missing AND netcommon absent — the declaration error must win.
	_, err := connectionVars(&connectionParams{Type: ConnNetworkCLI}, nil, "", false, noMount, noEE, fakeStage)
	if err == nil || !strings.Contains(err.Error(), "networkOS") {
		t.Fatalf("the fixable declaration error must be reported first, got %v", err)
	}
	if strings.Contains(err.Error(), "ansible.netcommon") {
		t.Error("…and it must not bury the fix under an image rebuild instruction")
	}
}

// A manifest that lists netcommon among other collections resolves it, and the version is
// read without being required to match anything — the check is presence, not a pin. Pins are
// the lockfile's job (ADR-0117 follow-up i) and duplicating them here would be a second
// authority over the same fact.
func TestPresenceIsTheQuestionNotTheVersion(t *testing.T) {
	have, err := eeCollections(netEE)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := have["ansible.netcommon"]; !ok {
		t.Fatalf("netcommon not found in %v", have)
	}
	if describeCollections(map[string]string{}) != "none" {
		t.Error("an empty install set must read as `none`, not as a truncated message")
	}
}
