package desiredstate

import (
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/provision"
)

// These cover the teardown half of the advertised-Workflow check and the identity choice behind the
// decommission Finding's launch spec (ADR-0114 D4 joining ADR-0120 D1).
//
// Both exist because the teardown reach-path was checked by NOTHING before the Finding carried a
// launch spec: `validateDecommissions` verifies an entry is non-empty and stops there, and since
// nothing launched the Workflow from the Finding, a mismatched or missing teardown Workflow was
// invisible until an operator tried to destroy something.

// A `decommissions:` entry naming a Workflow nobody declared is the teardown twin of the defect
// ADR-0120 D3 found on the build side (opentofu advertising a builder that was never written).
func TestDanglingTeardownTargetIsRefused(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: v\nselector: {kinds: [host]}\n")
	writeKind(t, root, "intents", "i.yaml",
		"name: web-fleet\nkind: Intent/Compute\nonRemove: remove\nspec:\n  count: 1\n  namePrefix: web\n"+
			"  projectKind: host\n  labels: {fleet: web}\n  requires: [provisioning]\n")
	writeKind(t, root, "actuators", "a.yaml",
		"name: awsec2\naddress: stratt-awsec2:9090\npluginIdentity: awsec2\ntier: trusted\n"+
			"provides: [provisioning]\nprovisions: {Compute: compute-build}\n"+
			"decommissions: {Compute: nowhere-teardown}\n")
	writeKind(t, root, "workflows", "w.yaml", "name: compute-build\n"+goodBuildInputs()+
		"steps:\n  - {name: approve, gate: {approvers: {teams: [platform-admins]}, timeoutSeconds: 3600}}\n"+
		"  - {name: s, needs: [approve], viewName: v, actuator: script, params: {script: echo}}\n")

	err := ParseDir2Err(t, root)
	if err == nil {
		t.Fatal("advertising a teardown Workflow that is not declared must be refused")
	}
	for _, want := range []string{"nowhere-teardown", "teardown", "no such"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name the missing Workflow and the act; want %q in: %v", want, err)
		}
	}
}

// A teardown Workflow must accept what the decommission reconcile sends. The schema is closed, so an
// undeclared input is a refused launch — discovered, before this check, at the moment of destruction.
func TestTeardownWorkflowMustFitWhatTheReconcileSends(t *testing.T) {
	err := ParseDir2Err(t, teardownEstate(t,
		"inputs:\n  type: object\n  additionalProperties: false\n  required: [identityValue]\n"+
			"  properties:\n    identityValue: {type: string}\n"))
	if err == nil {
		t.Fatal("a teardown Workflow that does not declare every supplied input must be refused")
	}
	// Reported in sorted order, so `identityScheme` is the first of the four undeclared keys.
	for _, want := range []string{"identityScheme", "is not a declared input", "tears down"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name the undeclared input and the act; want %q in: %v", want, err)
		}
	}
}

// §5 Flow: destructive ⇒ gated. A teardown without an approval Step is the sharper half of the same
// rule the build side already pins — launching it destroys infrastructure with no approval anywhere.
func TestTeardownWorkflowMustBeGated(t *testing.T) {
	err := ParseDir2Err(t, teardownEstate(t, goodTeardownInputs(), "ungated"))
	if err == nil {
		t.Fatal("an ungated teardown Workflow must be refused")
	}
	for _, want := range []string{"approval gate", "tear down"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name the missing gate and the act; want %q in: %v", want, err)
		}
	}
}

// The well-formed case must PASS, or the checks above prove nothing but that everything fails.
func TestAWellFormedTeardownWorkflowIsAccepted(t *testing.T) {
	if err := ParseDir2Err(t, teardownEstate(t, goodTeardownInputs())); err != nil {
		t.Fatalf("a teardown Workflow declaring exactly what the reconcile sends must be accepted: %v", err)
	}
}

// soleIdentity is what decides whether a teardown Finding is launchable at all, so its refusals are
// worth pinning: it must never GUESS which identity names a destructive target.
func TestSoleIdentityRefusesToGuess(t *testing.T) {
	scheme, value, err := soleIdentity(graph.DecommissionCandidate{
		Name: "web-05", IdentityKeys: map[string]string{"vcenter.uuid": "421f-..."},
	})
	if err != nil || scheme != "vcenter.uuid" || value != "421f-..." {
		t.Fatalf("a single identity must resolve: %q %q %v", scheme, value, err)
	}

	if _, _, err := soleIdentity(graph.DecommissionCandidate{Name: "web-05"}); err == nil {
		t.Error("an Entity with no identity gives a teardown Workflow no target — must fail closed")
	}

	_, _, err = soleIdentity(graph.DecommissionCandidate{Name: "web-05", IdentityKeys: map[string]string{
		"vcenter.uuid": "421f-...", "aws.instance-id": "i-123",
	}})
	if err == nil {
		t.Fatal("two identities is a §2.4 tiebreak core must not make — must fail closed")
	}
	// Both schemes named, sorted, so the operator can see what the ambiguity actually is (§1.8).
	if !strings.Contains(err.Error(), "aws.instance-id") || !strings.Contains(err.Error(), "vcenter.uuid") {
		t.Errorf("the refusal must name every competing scheme: %v", err)
	}
}

// The launch params the reconcile sends and the keys the declaration-time check verifies come from
// ONE function, which is the only reason the check cannot go stale. Pin the key set itself, because
// adding a param without declaring it in every advertised teardown Workflow is the failure mode.
func TestTeardownLaunchParamsKeySet(t *testing.T) {
	got := provision.TeardownLaunchParams("web-fleet",
		provision.Instance{Name: "web-05", Intent: "web-fleet", Ordinal: 5}, "vcenter.uuid", "421f")
	want := map[string]any{
		"instance": "web-05", "intent": "web-fleet", "ordinal": 5,
		"identityScheme": "vcenter.uuid", "identityValue": "421f",
	}
	if len(got) != len(want) {
		t.Fatalf("the teardown launch shape changed — every advertised teardown Workflow must declare "+
			"the new key or its launch is refused; got %v want %v", sortedKeys(got), sortedKeys(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %#v, want %#v", k, got[k], v)
		}
	}
}

// --- fixtures ---

func goodBuildInputs() string {
	return "inputs:\n  type: object\n  additionalProperties: false\n  properties:\n" +
		"    instance: {type: string}\n    ordinal: {type: integer}\n    projectKind: {type: string}\n" +
		"    labels: {type: object, additionalProperties: true}\n"
}

func goodTeardownInputs() string {
	return "inputs:\n  type: object\n  additionalProperties: false\n  required: [identityValue]\n" +
		"  properties:\n    instance: {type: string}\n    intent: {type: string}\n" +
		"    ordinal: {type: integer}\n    identityScheme: {type: string}\n" +
		"    identityValue: {type: string}\n"
}

// teardownEstate is a minimal estate whose sole provider advertises both a (well-formed) builder and
// a teardown Workflow built from the given inputs block. Pass "ungated" to omit the approve Step.
func teardownEstate(t *testing.T, teardownInputs string, opts ...string) string {
	t.Helper()
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: v\nselector: {kinds: [host]}\n")
	writeKind(t, root, "intents", "i.yaml",
		"name: web-fleet\nkind: Intent/Compute\nonRemove: remove\nspec:\n  count: 1\n  namePrefix: web\n"+
			"  projectKind: host\n  labels: {fleet: web}\n  requires: [provisioning]\n")
	writeKind(t, root, "actuators", "a.yaml",
		"name: awsec2\naddress: stratt-awsec2:9090\npluginIdentity: awsec2\ntier: trusted\n"+
			"provides: [provisioning]\nprovisions: {Compute: compute-build}\n"+
			"decommissions: {Compute: compute-teardown}\n")
	writeKind(t, root, "workflows", "build.yaml", "name: compute-build\n"+goodBuildInputs()+
		"steps:\n  - {name: approve, gate: {approvers: {teams: [platform-admins]}, timeoutSeconds: 3600}}\n"+
		"  - {name: s, needs: [approve], viewName: v, actuator: script, params: {script: echo}}\n")

	gate := "steps:\n  - {name: approve, gate: {approvers: {teams: [platform-admins]}, timeoutSeconds: 3600}}\n" +
		"  - {name: s, needs: [approve], viewName: v, actuator: script, params: {script: echo}}\n"
	for _, o := range opts {
		if o == "ungated" {
			gate = "steps:\n  - {name: s, viewName: v, actuator: script, params: {script: echo}}\n"
		}
	}
	writeKind(t, root, "workflows", "teardown.yaml", "name: compute-teardown\n"+teardownInputs+gate)
	return root
}
