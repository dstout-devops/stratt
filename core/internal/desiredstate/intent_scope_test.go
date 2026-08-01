package desiredstate

import (
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// twoSubstrateEstate writes the estate this whole change exists for: ONE Intent kind, TWO
// provisioning providers on two substrates, and a capability-binding that picks one per
// environment. `intentExtra` is spliced into the Intent document so a case can add or omit the
// `environments` filter.
//
// The aws builder binds {{.launch.params.ami}}; the kubernetes one binds nothing beyond the
// correlation set. That asymmetry IS the real one — a Kubernetes host has no AMI — and it is
// what made an unscoped Intent have to carry AWS coordinates into a Kubernetes environment.
func twoSubstrateEstate(t *testing.T, intentExtra string) string {
	t.Helper()
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: v\nselector: {kinds: [host]}\n")
	writeKind(t, root, "environments", "dev.yaml", "name: dev\ndescription: dev\n")
	writeKind(t, root, "environments", "prod.yaml", "name: prod\ndescription: prod\n")

	writeKind(t, root, "intents", "i.yaml",
		"name: web-fleet\nkind: Intent/Compute\n"+intentExtra+"spec:\n"+
			"  count: 2\n  namePrefix: web\n  projectKind: host\n  labels: {fleet: web}\n"+
			"  requires: [provisioning]\n  params: {}\n")

	writeKind(t, root, "actuators", "awsec2.yaml",
		"name: awsec2\naddress: stratt-awsec2:9090\npluginIdentity: awsec2\ntier: trusted\n"+
			"substrate: aws\nprovides: [provisioning]\nprovisions: {Compute: compute-build}\n")
	writeKind(t, root, "actuators", "kubecompute.yaml",
		"name: kubecompute\naddress: stratt-kubecompute:9090\npluginIdentity: kubecompute\ntier: trusted\n"+
			"substrate: kubernetes\nprovides: [provisioning]\nprovisions: {Compute: kubecompute-build}\n")

	// One line per environment selects the substrate — the shape ADR-0151 D2 ships.
	writeKind(t, root, "capability-bindings", "dev.yaml",
		"name: prov-dev\nenvironments: [dev]\n"+
			"entries:\n  - {capability: provisioning, substrate: kubernetes, intentKind: Compute}\n")
	writeKind(t, root, "capability-bindings", "prod.yaml",
		"name: prov-prod\nenvironments: [prod]\n"+
			"entries:\n  - {capability: provisioning, substrate: aws, intentKind: Compute}\n")

	gate := "steps:\n  - {name: approve, gate: {approvers: {teams: [platform-admins]}, timeoutSeconds: 3600}}\n"
	const inputs = "inputs:\n  type: object\n  additionalProperties: false\n  properties:\n" +
		"    instance: {type: string}\n    projectKind: {type: string}\n    labels: {type: object}\n"
	writeKind(t, root, "workflows", "kube.yaml",
		"name: kubecompute-build\n"+inputs+gate+
			"  - {name: s, needs: [approve], viewName: v, actuator: script, "+
			"params: {script: \"echo {{.launch.instance}} {{.launch.projectKind}} {{.launch.labels}}\"}}\n")
	writeKind(t, root, "workflows", "aws.yaml",
		"name: compute-build\n"+inputs+
			"    params: {type: object}\n"+gate+
			"  - {name: s, needs: [approve], viewName: v, actuator: script, "+
			"params: {script: \"echo {{.launch.instance}} {{.launch.projectKind}} {{.launch.labels}} {{.launch.params.ami}}\"}}\n")
	return root
}

// An Intent scoped to the environment whose binding selects Kubernetes is checked against the
// KUBERNETES builder only — so it may declare exactly the params that builder needs, and nothing
// else.
//
// This is the whole point of the change. Before it, the candidate set was the union of every
// declared provider's builder in every environment at once, so this Intent had to satisfy the AWS
// builder too — which is why the shipped `app-tier` carries `region`, `instanceType` and `ami`
// into a Kubernetes environment where nothing reads them.
func TestAScopedIntentIsCheckedOnlyAgainstItsEnvironmentsBuilders(t *testing.T) {
	root := twoSubstrateEstate(t, "environments: [dev]\n")
	if _, err := ParseDir(root, nil); err != nil {
		t.Fatalf("an Intent scoped to the Kubernetes environment must not be required to satisfy "+
			"the AWS builder — that is the coupling this scope removes: %v", err)
	}
}

// The control. The SAME Intent unscoped is refused, naming the AWS builder — proving the scope is
// what did it, and not that the check quietly stopped working.
func TestTheSameIntentUnscopedStillMustSatisfyEveryBuilder(t *testing.T) {
	root := twoSubstrateEstate(t, "")
	_, err := ParseDir(root, nil)
	if err == nil {
		t.Fatal("an UNSCOPED Intent is in force in every environment, so it must still satisfy " +
			"every substrate's builder — including the AWS one it declares no params for")
	}
	if !strings.Contains(err.Error(), "compute-build") {
		t.Fatalf("the refusal must name the builder that cannot be satisfied (§1.8); got: %v", err)
	}
}

// An Intent scoped to the AWS environment must satisfy the AWS builder — the scope narrows the
// candidate set, it does not excuse the Intent from the one builder that will actually run.
func TestScopingDoesNotExcuseTheBuilderThatWillRun(t *testing.T) {
	root := twoSubstrateEstate(t, "environments: [prod]\n")
	_, err := ParseDir(root, nil)
	if err == nil {
		t.Fatal("an Intent scoped to the AWS environment declares no ami and must be REFUSED — " +
			"narrowing the candidate set must not turn into skipping the check")
	}
	if !strings.Contains(err.Error(), "compute-build") {
		t.Fatalf("the refusal must name the AWS builder; got: %v", err)
	}
}

// ScopeToEnvironment must actually filter Intents, or the field parses, validates and changes
// nothing — an `environments: [dev]` a prod daemon still builds. An APPLICATION Intent reaches the
// estate through an Assignment which is already filtered, but a PROVISIONING Intent has no
// Assignment at all (ADR-0058 selects it by name), so this is the only place its scope applies.
func TestScopeToEnvironmentFiltersIntents(t *testing.T) {
	d := Declarations{Intents: []types.Intent{
		{Name: "dev-only", Kind: types.IntentCompute, Environments: []string{"dev"}},
		{Name: "prod-only", Kind: types.IntentCompute, Environments: []string{"prod"}},
		{Name: "everywhere", Kind: types.IntentCompute},
	}}
	got := map[string]bool{}
	for _, in := range ScopeToEnvironment(d, "dev").Intents {
		got[in.Name] = true
	}
	if !got["dev-only"] || !got["everywhere"] || got["prod-only"] {
		t.Fatalf("a dev daemon must see dev-only and the unscoped Intent and NOT prod-only; got %v", got)
	}
	if n := len(ScopeToEnvironment(d, "").Intents); n != 3 {
		t.Fatalf("an unscoped daemon sees every Intent; got %d", n)
	}
}

// A typo'd environment on an Intent is the quietest of all: the Intent has no Assignment, so an
// unknown scope does not narrow its reach — it removes the ONLY document in its path, and the fleet
// is never built anywhere with nothing raised. (The reflection-driven coverage test already demands
// SOME diagnostic; this pins that it names the Intent and the bad value.)
func TestATypoedIntentEnvironmentIsRefused(t *testing.T) {
	d := Declarations{
		Environments: []types.Environment{{Name: "dev"}},
		Intents:      []types.Intent{{Name: "web-fleet", Environments: []string{"dev1"}}},
	}
	err := validateEnvironmentRefs(d)
	if err == nil {
		t.Fatal("an Intent scoped to an undeclared environment must be refused at load")
	}
	for _, want := range []string{"intent", "web-fleet", "dev1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the diagnostic must contain %q; got: %v", want, err)
		}
	}
}

// ── The four findings from the charter-guardian review of this change ───────────────────

// VIOLATION 2's reproduction, kept as a test. The candidate set must never be SMALLER than what
// the reconcile could actually resolve to.
//
// An Intent scoped [dev] whose only provider is scoped [prod] produced an EMPTY candidate set —
// nothing checked at all — while an UNSCOPED daemon (STRATT_ENVIRONMENT empty, the default and
// what every demo estate runs) sees every provider and resolves that builder happily. The failure
// then landed at launch, after an operator approved the gate: the §1.8 regression this whole check
// exists to prevent, arriving through the door the change had just opened.
func TestTheUnscopedDaemonIsAlwaysACandidateEnvironment(t *testing.T) {
	decls := Declarations{
		Environments: []types.Environment{{Name: "dev"}, {Name: "prod"}},
		Actuators: []types.Actuator{{
			Name: "awsec2", Provides: []string{types.CapProvisioning}, Substrate: "aws",
			Provisions: map[string]string{"Compute": "compute-build"}, Environments: []string{"prod"},
		}},
	}
	in := types.Intent{Name: "web-fleet", Kind: types.IntentCompute, Environments: []string{"dev"}}
	got := reachableBuilders(decls, in, "Compute", false)
	if len(got) == 0 {
		t.Fatal("an Intent whose only provider is scoped elsewhere must still be checked against " +
			"that builder — an unscoped daemon sees both and will resolve it, so an empty candidate " +
			"set means the check silently does nothing (§1.8)")
	}
	if got[0] != "compute-build" {
		t.Fatalf("want compute-build in the candidate set; got %v", got)
	}
}

// FLAG A. A scoped Intent placed in a Subnet scoped elsewhere passes the name check and then fails
// identically at reconcile — the target is filtered out, nothing correlates, and the build surfaces
// forever as "build app-subnet first". The ability to express that did not exist until an Intent
// could carry a scope, so the check arrives with the field.
func TestAPlacementTargetMustCoverThePlacingIntentsScope(t *testing.T) {
	decls := Declarations{
		Environments: []types.Environment{{Name: "dev"}, {Name: "prod"}},
		Intents: []types.Intent{
			{Name: "app-subnet", Kind: types.IntentSubnet, Environments: []string{"prod"}},
			{Name: "app-tier", Kind: types.IntentCompute, Environments: []string{"dev"},
				Spec: map[string]any{"placement": map[string]any{"subnet": "app-subnet"}}},
		},
	}
	err := checkPlacementTargets(decls)
	if err == nil {
		t.Fatal("a dev Intent placed in a prod Subnet must be refused — the target is filtered out " +
			"of the dev reconcile and can never correlate")
	}
	for _, want := range []string{"app-tier", "app-subnet", "dev"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the diagnostic must name %q; got: %v", want, err)
		}
	}

	// An UNSCOPED target covers every scope and is always fine — the rule is coverage, not equality.
	decls.Intents[0].Environments = nil
	if err := checkPlacementTargets(decls); err != nil {
		t.Fatalf("an unscoped Subnet covers every environment: %v", err)
	}
}

// FLAG B. On an assignable kind the filter is inert at best and a second, disagreeing scope at
// worst: the compiler resolves the Intent from the store by NAME, and that read does not filter,
// so a prod Assignment would compile against a [dev]-scoped Intent anyway.
func TestAnAssignableIntentMayNotCarryEnvironments(t *testing.T) {
	err := ValidateIntent(types.Intent{
		Name: "tls-app", Kind: types.IntentApplication, Environments: []string{"dev"},
		Spec: map[string]any{"package": "apache"},
	})
	if err == nil {
		t.Fatal("an Application Intent is bound by an env-scoped Assignment; a second filter here " +
			"is redundant or contradictory and must be refused")
	}
	if !strings.Contains(err.Error(), "Assignment") {
		t.Fatalf("the diagnostic must point at the document that DOES carry the scope; got: %v", err)
	}
	// The provisioning kinds it exists for are unaffected.
	if err := ValidateIntent(types.Intent{
		Name: "web-fleet", Kind: types.IntentCompute, Environments: []string{"dev"},
		Spec: map[string]any{"count": 1, "namePrefix": "web", "projectKind": "host",
			"requires": []any{"provisioning"}},
	}); err != nil {
		t.Fatalf("a provisioning Intent is exactly what the filter is for: %v", err)
	}
}

// The env-keyed-values guardrail, extended to the Intent now that it is environment-aware. Same
// rule as rejectEnvKeyedValues, same deliberate narrowness.
func TestEnvKeyedParamsAreRefusedOnAnIntent(t *testing.T) {
	err := ValidateIntent(types.Intent{
		Name: "web-fleet", Kind: types.IntentCompute, Environments: []string{"dev", "prod"},
		Spec: map[string]any{"count": 1, "namePrefix": "web", "projectKind": "host",
			"requires": []any{"provisioning"},
			"params":   map[string]any{"dev": map[string]any{"region": "us-east-1"}}},
	})
	if err == nil {
		t.Fatal("params keyed by this Intent's own environment is the env-conditional-values " +
			"non-goal (§2.4, ADR-0118 D1) and must be refused")
	}
	if !strings.Contains(err.Error(), "membership filter") {
		t.Fatalf("the diagnostic must say why, not just that; got: %v", err)
	}
}
