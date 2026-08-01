package desiredstate

import (
	"testing"
)

// Where an Action Step's Contract is required to EXIST (ADR-0031, §2.2 — "an
// uncontracted operation must not exist"), and where it is only required to be
// CONSISTENT if present.
//
// The rule is enforced in three places, and which place matters:
//
//   - the REPO (TestEveryStepActionIsContracted) — every tree is on disk there, so
//     existence is exact and a typo'd Action name fails at the diff;
//   - RUNTIME (ValidateActionInput) — an uncontracted Invoke is refused outright;
//   - LOAD (here) — shape only, for the reason the next test records.

// actionStepEstate writes an estate with one Action Step using the given action and
// params body (raw YAML for the params block).
func actionStepEstate(t *testing.T, action, paramsYAML string) string {
	t.Helper()
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	writeKind(t, root, "workflows", "w.yaml",
		"name: wf\n"+
			"inputs:\n  type: object\n  additionalProperties: false\n"+
			"  properties:\n    value: {type: string}\n    name: {type: string}\n"+
			"steps:\n  - name: go\n    action: "+action+"\n    params:\n"+paramsYAML)
	return root
}

// TestUnknownActionWithTemplatedParamsLoads pins a DELIBERATE permissiveness, and it
// is the inverse of what this file asserted for one day.
//
// The load-time existence check was tried and reverted, because a running daemon's
// contract set is INCOMPLETE when it parses declarations: a plugin's self contracts
// arrive from its own tree only for the owning or an admitted plugin (ADR-0138
// D3/D4), and an environment-wired plugin — the boot-wired path every plugin demo
// uses — contributes its contracts later still, from its Manifest at enable. So "no
// contract" at load usually means "not yet", and enforcing it made strattd refuse to
// BOOT against a valid estate. `task ci` could not see that, because in-repo every
// plugin tree is on disk and complete; the vsphere demo floor found it.
//
// Nothing is lost. TestEveryStepActionIsContracted holds the rule where it is exact,
// and ValidateActionInput refuses the Invoke.
func TestUnknownActionWithTemplatedParamsLoads(t *testing.T) {
	root := actionStepEstate(t, "notyet/registered", "      key: \"{{.launch.value}}\"\n")
	if _, err := ParseDir(root, nil); err != nil {
		t.Fatalf("an Action whose Contract is not registered YET must load — a plugin wired by "+
			"environment registers its contracts after this parse, and refusing here stops the daemon booting: %v", err)
	}
}

// TestUncontractedActionIsRefusedWithConcreteParams: the case that already worked,
// kept so a refactor cannot quietly move the check behind the template guard again.
func TestUncontractedActionIsRefusedWithConcreteParams(t *testing.T) {
	root := actionStepEstate(t, "nosuch/operation", "      key: value\n")
	if _, err := ParseDir(root, nil); err == nil {
		t.Fatal("an uncontracted Action with concrete params must stay refused")
	}
}

// TestContractedActionWithTemplatedParamsStillLoads is the counterweight, and it is the
// reason the early return exists at all: a real Action whose params are only knowable at
// launch must still load. Break this and every {{.launch.x}}-parameterised Step in the
// estate fails, which is a far worse outcome than the hole being closed.
func TestContractedActionWithTemplatedParamsStillLoads(t *testing.T) {
	// netbox/ipam-resolve ships input+output contracts and is invoked with templated
	// params by the shipped vsphere-subnet-build Workflow.
	// cert-issuer/create-intermediate is CORE-SHIPPED and stays so: `cert-issuer` is a NEUTRAL
	// Actuator name (§1.5 — a step-ca plugin could implement it), which makes its contracts a
	// SEAM rather than one vendor's self contract (ADR-0138 D3). netbox's used to serve here and
	// no longer can: since the relocation, an estate that admits no plugins holds no plugin
	// contracts — the authority model working, not a regression.
	root := actionStepEstate(t, "cert-issuer/create-intermediate", "      commonName: \"{{.launch.name}}\"\n")
	if _, err := ParseDir(root, nil); err != nil {
		t.Fatalf("a contracted Action with templated params must still load — its VALUES are checked at launch: %v", err)
	}
}
