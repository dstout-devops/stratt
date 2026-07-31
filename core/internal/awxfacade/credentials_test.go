package awxfacade

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func machineRef() types.CredentialRef {
	return types.CredentialRef{
		Name: "web-machine", OwnerTeam: "platform", Backend: "vault",
		Locator: json.RawMessage(`{"mount":"kv","path":"estate/web-machine","kvV2":true}`),
		Injection: []types.CredentialInjection{
			{Key: "ssh_private_key", As: "file", Name: "id_ed25519"},
			{Key: "become_password", As: "env", Name: "ANSIBLE_BECOME_PASS"},
		},
		DeclaredBy: "cac",
	}
}

// THE §2.5 LINE, asserted on the surface most likely to erode it. `inputs` carries declared field
// NAMES with AWX's "a secret stands here" sentinel — never a value, and there is no value in scope
// to write: the only input to the renderer is a CredentialRef, which has no material field. This
// test pins the rendering; the type system pins everything else.
func TestCredentialInputsAreKeyNamesNeverValues(t *testing.T) {
	got := credentialRefToCredential(machineRef())
	inputs := got["inputs"].(map[string]any)
	if len(inputs) != 2 {
		t.Fatalf("both declared fields must appear — hiding a field name protects nothing and hides "+
			"which fields the ref projects: %v", inputs)
	}
	for k, v := range inputs {
		if v != "$encrypted$" {
			t.Errorf("inputs[%q] = %v — the only legal value is the sentinel", k, v)
		}
	}

	// Nothing anywhere in the rendering may carry the locator: it is not material, but it is the
	// ADDRESS of material, and a compat listing is not the place to widen who reads it.
	blob, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"estate/web-machine", "kvV2", "locator"} {
		if strings.Contains(string(blob), leak) {
			t.Errorf("the rendered credential carries %q — /api/v1 serves the locator under its own "+
				"authz; this surface does not: %s", leak, blob)
		}
	}
}

// The broker coordinates an operator DOES need — which backend, whose team, how each field lands —
// ride a namespaced descent block rather than an AWX field they would be mistaken for (§1.8).
func TestCredentialCarriesTheBrokerFactsWithoutTheAddress(t *testing.T) {
	sf := credentialRefToCredential(machineRef())["summary_fields"].(map[string]any)["stratt"].(map[string]any)
	if sf["backend"] != "vault" || sf["owner_team"] != "platform" || sf["declared_by"] != "cac" {
		t.Errorf("broker facts missing: %v", sf)
	}
	if sf["gate_only"] != false {
		t.Error("a ref with injections is not gate-only")
	}
	injects := sf["injects"].([]map[string]any)
	if len(injects) != 2 || injects[0]["as"] != "file" || injects[1]["name"] != "ANSIBLE_BECOME_PASS" {
		t.Errorf("HOW a field is projected is the operational question a reader has: %v", injects)
	}
}

// A gate-only ref brokers NOTHING (ADR-0052/0092). An empty `inputs` alone would read as "not yet
// configured", which is a different and wrong diagnosis.
func TestGateOnlyRefSaysItBrokersNothing(t *testing.T) {
	ref := types.CredentialRef{Name: "vsphere-decommission-gate", OwnerTeam: "platform",
		Backend: "k8s-secret", GateOnly: true}
	got := credentialRefToCredential(ref)
	desc := got["description"].(string)
	if !strings.Contains(desc, "GATE-ONLY") {
		t.Errorf("an injection-less ref must say it authorizes and nothing more: %q", desc)
	}
	if len(got["inputs"].(map[string]any)) != 0 {
		t.Error("a gate-only ref projects no fields")
	}
	sf := got["summary_fields"].(map[string]any)["stratt"].(map[string]any)
	if sf["gate_only"] != true {
		t.Error("gate_only must be machine-readable too, not only in prose")
	}
}

// The credential_type reference must RESOLVE. A dangling id is the failure this package refused for
// workflow nodes and must not reintroduce here.
func TestCredentialTypeReferenceResolves(t *testing.T) {
	cred := credentialRefToCredential(machineRef())
	if cred["credential_type"] != credentialTypeID() {
		t.Fatalf("credential_type = %v, want %d", cred["credential_type"], credentialTypeID())
	}
	ct := credentialType()
	if ct["id"] != credentialTypeID() || ct["name"] != credentialTypeName {
		t.Fatalf("the type served at that id is not the one referenced: %v", ct)
	}
	// AWX permits only "cloud" or "net" on a CUSTOM type — "cloud" is a constraint, not a claim.
	if ct["kind"] != "cloud" && ct["kind"] != "net" {
		t.Errorf("kind = %v, which AWX rejects on a custom credential type", ct["kind"])
	}
	fields := ct["inputs"].(map[string]any)["fields"].([]any)
	if len(fields) != 0 {
		t.Error("a CredentialRef's projected fields are declared PER REF in Git, so there is no " +
			"per-type schema; a union of every ref's keys would describe a type no ref has")
	}
}

// One type for every ref, because AWX's credential_type says what a credential is FOR and Stratt's
// backend says WHO BROKERS IT. Mapping one onto the other would be a category error that reads as
// fact — a client filtering on credential_type would think it had filtered by purpose.
func TestBackendDoesNotBecomeTheCredentialType(t *testing.T) {
	vault := machineRef()
	k8s := machineRef()
	k8s.Name, k8s.Backend = "aws-dev", "k8s-secret"
	a, b := credentialRefToCredential(vault), credentialRefToCredential(k8s)
	if a["credential_type"] != b["credential_type"] {
		t.Error("two backends must not become two AWX credential types — they are a different axis")
	}
	if a["id"] == b["id"] {
		t.Error("distinct refs must have distinct ids")
	}
}
