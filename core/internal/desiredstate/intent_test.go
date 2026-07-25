package desiredstate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func writeKind(t *testing.T, root, kind, file, content string) {
	t.Helper()
	dir := filepath.Join(root, kind)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseIntentLayer(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: dev-vms\nselector: {kinds: [vm]}\n")
	writeKind(t, root, "intents", "chrome.yaml", `
name: chrome
kind: Intent/Application
spec: { package: google-chrome, channel: stable }
onRemove: retain
`)
	writeKind(t, root, "blueprints", "app-v3.yaml", `
name: application
version: 3
for: Intent/Application
severity: warning
dampingObservations: 2
routes:
  - match:
      - { namespace: os.kernel, path: family, equals: linux }
    observe: { namespace: apps.installed, contains: "{{.spec.package}}" }
    claim: additive
`)
	writeKind(t, root, "assignments", "kiosks.yaml", `
name: kiosks
intent: chrome@1
view: dev-vms
blueprint: application@3
environments: [prod]
maxDelta: 0.4
`)
	parsed, err := ParseDir(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Intents) != 1 || parsed.Intents[0].Kind != types.IntentApplication {
		t.Fatalf("intents: %+v", parsed.Intents)
	}
	if len(parsed.Blueprints) != 1 {
		t.Fatalf("blueprints: %+v", parsed.Blueprints)
	}
	bp := parsed.Blueprints[0]
	if bp.Name != "application" || bp.Version != 3 || len(bp.Routes) != 1 {
		t.Fatalf("blueprint: %+v", bp)
	}
	if bp.Routes[0].Claim != types.ClaimAdditive || string(bp.Routes[0].Observe.Contains) != `"{{.spec.package}}"` {
		t.Fatalf("route: %+v", bp.Routes[0])
	}
	if string(bp.Routes[0].Match[0].Equals) != `"linux"` {
		t.Fatalf("match equals must canonicalize to JSON: %s", bp.Routes[0].Match[0].Equals)
	}
	a := parsed.Assignments[0]
	if a.Blueprint != "application" || a.BlueprintVersion != 3 || a.MaxDelta == nil || *a.MaxDelta != 0.4 {
		t.Fatalf("assignment: %+v", a)
	}

	// Rejections.
	for name, docs := range map[string]map[string]string{
		"unimplemented kind": {"intents": "name: x\nkind: Intent/Config\nspec: {}\n"}, // charter-named, no schema yet
		// NOTE: "invalid cert spec" (Intent/Certificate with spec: {}) used to live here.
		// ADR-0118 D1 moved Intent spec validation at DECLARATION from complete to PARTIAL,
		// because a layered spec means an Intent may legitimately omit a field its
		// Assignment's `values` supply — so an incomplete fragment must now parse.
		// Completeness is enforced once on the MERGED spec at compile; the rejection is
		// pinned there by compiler.TestValidateResolvedSpecRejectsAnIncompleteSpec, and
		// TestIncompleteIntentSpecParsesButIsNotComplete below pins this half.
		"bad cert field type": {"intents": "name: x\nkind: Intent/Certificate\nspec: {issuer: 7}\n"}, // present ⇒ still typed
		"remove on non-cert":  {"intents": "name: x\nkind: Intent/Application\nonRemove: remove\n"},
		"revert on non-file":  {"intents": "name: x\nkind: Intent/Application\nonRemove: revert\n"}, // Application supports neither
		"blueprint no ver":    {"blueprints": "name: b\nfor: Intent/Application\nroutes: [{observe: {namespace: n, equals: 1}, claim: additive}]\n"},
		"blueprint bad kind":  {"blueprints": "name: b\nversion: 1\nfor: Intent/Config\nroutes: [{observe: {namespace: n, equals: 1}, claim: additive}]\n"},
		"bad claim":           {"blueprints": "name: b\nversion: 1\nfor: Intent/Application\nroutes: [{observe: {namespace: n, equals: 1}, claim: priority}]\n"},
		"bad blueprint ref":   {"assignments": "name: a\nintent: i@1\nview: v\nblueprint: application\n"},
	} {
		bad := t.TempDir()
		writeDecl(t, bad, "v.yaml", "name: v\nselector: {kinds: [vm]}\n")
		for kind, doc := range docs {
			writeKind(t, bad, kind, "x.yaml", doc)
		}
		if _, err := ParseDir(bad, nil); err == nil {
			t.Fatalf("invalid intent-layer (%s) must be rejected", name)
		}
	}
}

// TestCertificateIntentGA proves the Phase-3 kind is now first-class: a valid
// Intent/Certificate with onRemove: remove and a Certificate Blueprint (with a
// notBefore expiry threshold + removeWorkflow) parse cleanly (ADR-0030).
func TestCertificateIntentGA(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: certs\nselector: {kinds: [cert]}\n")
	writeKind(t, root, "intents", "c.yaml",
		"name: web-cert\nkind: Intent/Certificate\nonRemove: remove\n"+
			"spec: {issuer: cert-issuer/stratt-dev, commonName: web.stratt.test, renewBefore: 360h, exportable: false}\n")
	writeKind(t, root, "blueprints", "b.yaml",
		"name: certificate\nversion: 1\nfor: Intent/Certificate\nseverity: warning\nremoveWorkflow: cert-revoke\n"+
			"routes: [{observe: {namespace: cert.expiry, path: notAfter, notBefore: '{{.spec.renewBefore}}'}, claim: exclusive, remediationWorkflow: cert-renew}]\n")
	parsed, err := ParseDir(root, nil)
	if err != nil {
		t.Fatalf("valid certificate intent-layer must parse: %v", err)
	}
	if in := parsed.Intents[0]; in.Kind != types.IntentCertificate || in.OnRemove != types.OnRemoveRemove {
		t.Fatalf("intent: %+v", in)
	}
	if bp := parsed.Blueprints[0]; bp.For != types.IntentCertificate || bp.RemoveWorkflow != "cert-revoke" ||
		bp.Routes[0].Observe.NotBefore != "{{.spec.renewBefore}}" {
		t.Fatalf("blueprint: %+v", bp)
	}
}

// TestFileSetIntentGA proves the Intent/FileSet kind parses, accepts
// onRemove: revert (ADR-0036), and drives a digest-Equals Blueprint route.
func TestFileSetIntentGA(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	writeKind(t, root, "intents", "f.yaml",
		"name: nginx-conf\nkind: Intent/FileSet\nonRemove: revert\n"+
			"spec: {key: nginx-conf, path: /etc/nginx/nginx.conf, digest: 'sha256:"+
			"0000000000000000000000000000000000000000000000000000000000000000', mode: '0644', owner: root}\n")
	writeKind(t, root, "blueprints", "b.yaml",
		"name: fileset\nversion: 1\nfor: Intent/FileSet\nseverity: warning\nremoveWorkflow: fileset-revert\n"+
			"routes: [{observe: {namespace: fileset.content, path: '{{.spec.key}}.digest', equals: '{{.spec.digest}}'}, claim: additive, remediationWorkflow: fileset-apply}]\n")
	parsed, err := ParseDir(root, nil)
	if err != nil {
		t.Fatalf("valid fileset intent-layer must parse: %v", err)
	}
	if in := parsed.Intents[0]; in.Kind != types.IntentFileSet || in.OnRemove != types.OnRemoveRevert {
		t.Fatalf("intent: %+v", in)
	}
	if bp := parsed.Blueprints[0]; bp.For != types.IntentFileSet || bp.RemoveWorkflow != "fileset-revert" ||
		bp.Routes[0].Observe.Namespace != "fileset.content" {
		t.Fatalf("blueprint: %+v", bp)
	}
	// A bad digest (not sha256:<64hex>) is refused at the seam (§1.1).
	writeKind(t, root, "intents", "f.yaml",
		"name: nginx-conf\nkind: Intent/FileSet\nspec: {key: nginx-conf, path: /etc/nginx/nginx.conf, digest: nope}\n")
	if _, err := ParseDir(root, nil); err == nil {
		t.Fatal("fileset spec with a malformed digest must be rejected")
	}
}

// TestAccessIntentGA proves the Intent/Access kind parses, accepts additive
// claims + onRemove: revert/remove (ADR-0036), and an ensures-contains route.
func TestAccessIntentGA(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	writeKind(t, root, "intents", "a.yaml",
		"name: alice-wheel\nkind: Intent/Access\nonRemove: revert\n"+
			"spec: {subject: alice, kind: group, scope: wheel}\n")
	writeKind(t, root, "blueprints", "b.yaml",
		"name: access\nversion: 1\nfor: Intent/Access\nseverity: warning\nremoveWorkflow: access-revoke\n"+
			"routes: [{observe: {namespace: access.grants, contains: {subject: '{{.spec.subject}}', kind: '{{.spec.kind}}', scope: '{{.spec.scope}}'}}, claim: additive, remediationWorkflow: access-apply}]\n")
	parsed, err := ParseDir(root, nil)
	if err != nil {
		t.Fatalf("valid access intent-layer must parse: %v", err)
	}
	if in := parsed.Intents[0]; in.Kind != types.IntentAccess || in.OnRemove != types.OnRemoveRevert {
		t.Fatalf("intent: %+v", in)
	}
	if bp := parsed.Blueprints[0]; bp.For != types.IntentAccess || bp.Routes[0].Claim != types.ClaimAdditive {
		t.Fatalf("blueprint: %+v", bp)
	}
	// onRemove: remove is also valid for Access (revoke a grant).
	writeKind(t, root, "intents", "a.yaml",
		"name: alice-wheel\nkind: Intent/Access\nonRemove: remove\nspec: {subject: alice, kind: group, scope: wheel}\n")
	if _, err := ParseDir(root, nil); err != nil {
		t.Fatalf("access onRemove: remove must parse: %v", err)
	}
}

func TestBlueprintVersionsCoexist(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: v\nselector: {kinds: [vm]}\n")
	base := "name: application\nfor: Intent/Application\nroutes: [{observe: {namespace: n, equals: 1}, claim: additive}]\nversion: "
	writeKind(t, root, "blueprints", "v1.yaml", base+"1\n")
	writeKind(t, root, "blueprints", "v2.yaml", base+"2\n")
	parsed, err := ParseDir(root, nil)
	if err != nil {
		t.Fatalf("two versions of one blueprint must coexist: %v", err)
	}
	if len(parsed.Blueprints) != 2 {
		t.Fatalf("blueprints: %+v", parsed.Blueprints)
	}
}

// Two versions of one INTENT are refused for now, and the refusal must name the mechanism
// rather than look like an authoring mistake (ADR-0119 D7, §1.8). Before this, the dedup key
// was the bare name, so the author got `"tls-app" declared in both v1.yaml and v2.yaml` — a
// message that says "you did this twice by accident" about something they meant to do, and
// sends them to delete a file instead of telling them rings are not available until the
// contract migration ships.
func TestTwoIntentVersionsAreRefusedByMechanismNotByDuplicateFile(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: v\nselector: {kinds: [vm]}\n")
	base := "name: tls-app\nkind: Intent/Application\nspec: {package: nginx, channel: stable}\nversion: "
	writeKind(t, root, "intents", "v1.yaml", base+"1\n")
	writeKind(t, root, "intents", "v2.yaml", base+"2\n")

	_, err := ParseDir(root, nil)
	if err == nil {
		t.Fatal("two versions of one Intent must be refused while the (name) primary key survives")
	}
	// The diagnosis, not just the refusal: the author must learn WHY it is refused, WHEN it
	// stops being refused, and WHAT to do meanwhile. A test on the presence of an error alone
	// would pass against the old misleading message.
	for _, want := range []string{"tls-app", "version 1", "version 2", "primary key", "Rings light up", "ONE commit"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must mention %q, got: %v", want, err)
		}
	}
	// And it must NOT read as a duplicate-file collision, which is a different defect with a
	// different fix.
	if strings.Contains(err.Error(), "declared in both") {
		t.Errorf("the refusal still reads as an accidental duplicate declaration: %v", err)
	}
}

// The same NAME and the same VERSION in two files is still an ordinary duplicate, and must
// still be caught — versioning the dedup key must not open a hole where one file silently
// wins over another.
func TestSameIntentNameAndVersionInTwoFilesIsStillADuplicate(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: v\nselector: {kinds: [vm]}\n")
	base := "name: tls-app\nkind: Intent/Application\nspec: {package: nginx, channel: stable}\nversion: 1\n"
	writeKind(t, root, "intents", "a.yaml", base)
	writeKind(t, root, "intents", "b.yaml", base)

	_, err := ParseDir(root, nil)
	if err == nil {
		t.Fatal("one Intent name+version declared in two files must be refused")
	}
	if !strings.Contains(err.Error(), "declared in both") {
		t.Fatalf("a genuine duplicate must still be reported as one: %v", err)
	}
}

// TestComputeOnRemoveDecommission proves ADR-0114 D4: onRemove:remove is now VALID for Intent/Compute
// (the decommission reach-path), where it was rejected before. It parses cleanly and carries the
// removal semantic through.
func TestComputeOnRemoveDecommission(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: v\nselector: {kinds: [host]}\n")
	writeKind(t, root, "intents", "c.yaml",
		"name: web-fleet\nkind: Intent/Compute\nonRemove: remove\n"+
			"spec: {count: 3, namePrefix: web, projectKind: host, requires: [provisioning]}\n")
	parsed, err := ParseDir(root, nil)
	if err != nil {
		t.Fatalf("Intent/Compute with onRemove:remove must parse (ADR-0114 D4): %v", err)
	}
	if in := parsed.Intents[0]; in.Kind != types.IntentCompute || in.OnRemove != types.OnRemoveRemove {
		t.Fatalf("parsed intent: kind=%s onRemove=%s", in.Kind, in.OnRemove)
	}
}

// TestIncompleteIntentSpecParsesButIsNotComplete pins the declaration half of ADR-0118
// D1's validation move, and states its cost plainly.
//
// An Intent may omit a field it leaves to its Assignment's `values`, so declaration-time
// validation is PARTIAL: every field present is still typed, but a missing `required`
// field no longer fails here. Completeness moved to the compiler, against the merged spec
// (compiler.validateResolvedSpec) — which is the only place all the layers exist.
//
// The cost, recorded rather than hidden: an Intent that NO Assignment references is never
// completeness-checked at all, because nothing ever merges it. That is tolerable only
// because such an Intent compiles no Baselines and therefore does nothing — but it does
// mean "it parsed" is a weaker statement than it used to be.
func TestIncompleteIntentSpecParsesButIsNotComplete(t *testing.T) {
	dir := t.TempDir()
	writeDecl(t, dir, "v.yaml", "name: v\nselector: {kinds: [vm]}\n")
	// Missing required issuer/commonName/renewBefore — a fragment the Assignment completes.
	writeKind(t, dir, "intents", "c.yaml", "name: c\nkind: Intent/Certificate\nspec: {commonName: web.test}\n")
	parsed, err := ParseDir(dir, nil)
	if err != nil {
		t.Fatalf("a partial Intent spec must PARSE (its Assignment may complete it): %v", err)
	}
	if len(parsed.Intents) != 1 || parsed.Intents[0].Spec["commonName"] != "web.test" {
		t.Fatalf("the fragment must survive parsing intact, got %+v", parsed.Intents)
	}
}

// TestAssignmentValuesParse pins the field ADR-0083 D1 promised ("plus optional
// overrides") and ADR-0118 D1 finally delivers: an Assignment carries parameter values,
// which the compiler merges as a co-equal declaration alongside the Intent's spec.
func TestAssignmentValuesParse(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	writeKind(t, root, "assignments", "a.yaml",
		"name: prod-web\nintent: web@1\nview: hosts\nblueprint: web-server@1\nenvironments: [prod]\n"+
			"values: {port: 8443, replicas: 6}\n")
	parsed, err := ParseDir(root, nil)
	if err != nil {
		t.Fatalf("an Assignment with values must parse: %v", err)
	}
	a := parsed.Assignments[0]
	if a.Values["port"] != 8443 || a.Values["replicas"] != 6 {
		t.Fatalf("values must survive parsing, got %+v", a.Values)
	}
}

// TestAssignmentRejectsEnvKeyedValues guards the creep vector ADR-0118 D1 names
// explicitly, because it is the first thing someone reaches for once per-environment
// values exist: `values: {prod: {...}, staging: {...}}`.
//
// types.EnvScoped already binds this — `environments` is a boolean MEMBERSHIP filter and
// "never a source of env-conditional values", which would be the new-configuration-language
// non-goal. The compliant shape is one Assignment per environment.
func TestAssignmentRejectsEnvKeyedValues(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	writeKind(t, root, "assignments", "a.yaml",
		"name: web\nintent: web@1\nview: hosts\nblueprint: web-server@1\nenvironments: [prod, staging]\n"+
			"values: {prod: {port: 443}, staging: {port: 8443}}\n")
	_, err := ParseDir(root, nil)
	if err == nil {
		t.Fatal("environment-keyed values must be rejected (§1 non-goal, EnvScoped contract)")
	}
	// §1.8: the message has to say which key and what to do instead.
	for _, want := range []string{"prod", "one Assignment"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must mention %q so the fix is obvious; got: %v", want, err)
		}
	}
}

// TestAssignmentValuesNotKeyedByAnUnrelatedEnvIsAllowed records the guardrail's honest
// limit: it fires only on a key matching one of THIS Assignment's own environments, so a
// legitimate value that happens to be named after some other environment still parses. It
// is a guardrail against the shape people write, not a proof.
func TestAssignmentValuesNotKeyedByAnUnrelatedEnvIsAllowed(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	writeKind(t, root, "assignments", "a.yaml",
		"name: web\nintent: web@1\nview: hosts\nblueprint: web-server@1\nenvironments: [prod]\n"+
			"values: {staging: something}\n")
	if _, err := ParseDir(root, nil); err != nil {
		t.Fatalf("a value key naming an environment this Assignment does not declare is not detectable here: %v", err)
	}
}

// ── Trigger → Workflow launch inputs (ADR-0118 D5) ────────────────────────────────────

// TestScheduleTriggerInputsCheckedAtDeclaration: a schedule has no firing event, so its inputs
// are literal values — which means they can be checked in Git review rather than when the
// schedule first fires at 3am.
//
// Note what the gap actually was. A Workflow-target Trigger could not parameterize its Workflow
// AT ALL: `params` are Step fields and are refused on a Workflow target ("the Workflow declares
// its own"), so the only Workflows a Trigger could launch were ones needing no inputs. That was
// harmless while launches accepted anything and fatal once `required` inputs are enforced — so
// `inputs` is a new field, not a resurrected one.
func TestScheduleTriggerInputsCheckedAtDeclaration(t *testing.T) {
	const wf = "name: build\ninputs:\n  type: object\n  additionalProperties: false\n" +
		"  required: [targetSubnet]\n  properties:\n    targetSubnet: {type: string}\n" +
		"steps:\n  - {name: s, viewName: v, actuator: script, params: {script: \"echo {{.launch.targetSubnet}}\"}}\n"

	// Satisfied: parses.
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: v\nselector: {kinds: [host]}\n")
	writeKind(t, root, "workflows", "b.yaml", wf)
	writeKind(t, root, "triggers", "t.yaml",
		"name: nightly\nkind: schedule\ncron: \"0 2 * * *\"\nworkflowName: build\nprincipal: svc\n"+
			"inputs: {targetSubnet: app-subnet}\n")
	if _, err := ParseDir(root, nil); err != nil {
		t.Fatalf("a schedule Trigger supplying the declared input must parse: %v", err)
	}

	// Unknown key: rejected, naming both the trigger and the workflow.
	bad := t.TempDir()
	writeDecl(t, bad, "v.yaml", "name: v\nselector: {kinds: [host]}\n")
	writeKind(t, bad, "workflows", "b.yaml", wf)
	writeKind(t, bad, "triggers", "t.yaml",
		"name: nightly\nkind: schedule\ncron: \"0 2 * * *\"\nworkflowName: build\nprincipal: svc\n"+
			"inputs: {targetSubnett: app-subnet}\n")
	err := ParseDir2Err(t, bad)
	if err == nil {
		t.Fatal("a schedule Trigger passing an undeclared input must be rejected at declaration")
	}
	for _, want := range []string{"nightly", "build"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name both documents; got: %v", err)
		}
	}

	// Missing a required input: also rejected — the schedule would fire and fail forever.
	missing := t.TempDir()
	writeDecl(t, missing, "v.yaml", "name: v\nselector: {kinds: [host]}\n")
	writeKind(t, missing, "workflows", "b.yaml", wf)
	writeKind(t, missing, "triggers", "t.yaml",
		"name: nightly\nkind: schedule\ncron: \"0 2 * * *\"\nworkflowName: build\nprincipal: svc\n"+
			"inputs: {}\n")
	// inputs: {} is empty, so the check skips it and the missing required input is caught by the
	// launch chokepoint at fire time instead. Recorded rather than asserted-as-working: an EMPTY
	// map is indistinguishable from "none declared", and refusing it here would reject every
	// Workflow-target Trigger that legitimately passes nothing.
	if _, err := ParseDir(missing, nil); err != nil {
		t.Logf("empty params currently parse; the chokepoint catches the missing required input: %v", err)
	}
}

// TestEventTriggerInputsNotCheckedAtDeclaration: an event Trigger's inputs carry {{.event.x}},
// which resolves only against a real payload — so the placeholder is not the value the schema
// must accept (ADR-0024 D4's reasoning). It must PARSE, and be validated after substitution.
func TestEventTriggerInputsNotCheckedAtDeclaration(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: v\nselector: {kinds: [host]}\n")
	writeKind(t, root, "emitters", "e.yaml", "name: em\nkind: webhook\ntokenHash: "+
		"0000000000000000000000000000000000000000000000000000000000000000\n")
	writeKind(t, root, "workflows", "b.yaml",
		"name: build\ninputs:\n  type: object\n  additionalProperties: false\n"+
			"  properties:\n    host: {type: string}\n"+
			"steps:\n  - {name: s, viewName: v, actuator: script, params: {script: \"echo {{.launch.host}}\"}}\n")
	writeKind(t, root, "triggers", "t.yaml",
		"name: on-event\nkind: event\nemitter: em\nwhen: \"true\"\nworkflowName: build\nprincipal: svc\n"+
			"inputs: {host: \"{{.event.hostname}}\"}\n")
	if _, err := ParseDir(root, nil); err != nil {
		t.Fatalf("an event Trigger's templated params must parse (resolved at launch): %v", err)
	}
}

func ParseDir2Err(t *testing.T, dir string) error {
	t.Helper()
	_, err := ParseDir(dir, nil)
	return err
}

// TestApplicationPortTypedAtTheIntentSeam pins the tightening ADR-0118 booked as a follow-up,
// and it exists because the loose version cost a live cluster round-trip.
//
// contracts/intents/application.schema.json was `additionalProperties: true` with only
// package/channel typed, so an Intent could declare `port: 443` (a number) and parse cleanly.
// The app.config Facet declares port as a STRING, so the mismatch surfaced only when a real Run
// tried to write the observed value back: "/port: got number, want string". Typed at this seam
// it fails at declaration.
//
// The schema stays OPEN on purpose — Intent/Application carries app-specific fields like the
// demo's commonName, and closing it would make core learn every application's configuration
// (§1.1/§9 ontology creep). So this asserts both halves: the typed field is enforced, and an
// untyped one is still allowed.
func TestApplicationPortTypedAtTheIntentSeam(t *testing.T) {
	write := func(t *testing.T, spec string) error {
		t.Helper()
		root := t.TempDir()
		writeDecl(t, root, "v.yaml", "name: v\nselector: {kinds: [host]}\n")
		writeKind(t, root, "intents", "a.yaml", "name: app\nkind: Intent/Application\nspec: "+spec+"\n")
		_, err := ParseDir(root, nil)
		return err
	}

	if err := write(t, "{package: nginx, port: \"443\"}"); err != nil {
		t.Fatalf("a string port must be accepted: %v", err)
	}
	err := write(t, "{package: nginx, port: 443}")
	if err == nil {
		t.Fatal("a NUMBER port must now be rejected at the Intent seam — the app.config Facet types it as a string, " +
			"so a number is unusable and used to fail only at facet write-back, after a full cluster round-trip")
	}
	if !strings.Contains(err.Error(), "port") {
		t.Errorf("the error must name the offending field; got: %v", err)
	}
	// Still open: an app-specific field core knows nothing about must pass.
	if err := write(t, "{package: nginx, commonName: app.example.test}"); err != nil {
		t.Fatalf("an app-specific field must still be allowed (the kind is generic, §1.1): %v", err)
	}
}

// ── ADR-0119: versioned configuration ─────────────────────────────────────────────────

// TestPinnedVersionCannotBeEditedInPlace is D6, the decision that makes "immutable once it passes
// go" true rather than aspirational — and it exists because the first draft of ADR-0119 assumed
// versioning alone delivered it.
//
// It does not: UpsertIntent updates a row in place, and computeIntentLayerPlan emits ActionUpdate
// for a same-version content edit. So editing an Intent WITHOUT touching `version:` would change
// what a pinned environment is running at the next reconcile, which is exactly the failure the ADR
// claimed was structurally impossible.
func TestPinnedVersionCannotBeEditedInPlace(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	base := Declarations{
		Views: []Declaration{{Name: "hosts", Selector: types.ViewSelector{Kinds: []string{"host"}}}},
		Intents: []types.Intent{{
			Name: "app-v", Kind: types.IntentApplication, Version: 1,
			Spec: map[string]any{"package": "nginx"},
		}},
		Assignments: []types.Assignment{{
			Name: "prod-v", Intent: "app-v", IntentVersion: 1, View: "hosts",
			Blueprint: "bp-v", BlueprintVersion: 1,
		}},
		Blueprints: []types.Blueprint{{
			Name: "bp-v", Version: 1, For: types.IntentApplication,
			Routes: []types.BlueprintRoute{{
				Observe: types.FacetExpectation{Namespace: "app.config", Path: "port", Equals: []byte(`"443"`)},
				Claim:   types.ClaimExclusive,
			}},
		}},
	}
	if _, err := Apply(ctx, s, base); err != nil {
		t.Fatalf("initial apply: %v", err)
	}

	// Same version, different content — the natural careless edit.
	edited := base
	edited.Intents = []types.Intent{{
		Name: "app-v", Kind: types.IntentApplication, Version: 1,
		Spec: map[string]any{"package": "apache"}, // changed under a pinned version
	}}
	plan, err := Apply(ctx, s, edited)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	var entry *PlanEntry
	for i := range plan.Entries {
		if plan.Entries[i].Kind == KindIntent && plan.Entries[i].Name == "app-v@1" {
			entry = &plan.Entries[i]
		}
	}
	if entry == nil {
		t.Fatal("no plan entry for intent app-v@1 — the plan key should be name@version")
	}
	if entry.Error == "" {
		t.Fatal("editing a PINNED version in place must be refused (ADR-0119 D6)")
	}
	for _, want := range []string{"prod-v", "new version"} {
		if !strings.Contains(entry.Error, want) {
			t.Errorf("the refusal must name the pinning Assignment and the fix; got: %s", entry.Error)
		}
	}
	// And the refusal must be BEHAVIOUR, not just a message: the stored spec is unchanged.
	stored, err := s.GetIntent(ctx, "app-v", 1)
	if err != nil {
		t.Fatalf("get intent: %v", err)
	}
	if stored.Spec["package"] != "nginx" {
		t.Fatalf("a refused edit must not be applied — stored spec is %v", stored.Spec)
	}
}

// TestPinnedVersionCannotBeDeleted: the other half of D6. The natural in-place bump
// (version: 1 → 2 in one file) produces Create @2 + Delete @1 in a single plan, and deleting @1
// while a ring still pins it makes that Assignment fail validateRefs — which RETAINS its prior
// Baselines, so the environment freezes on stale expectations while erroring. MaxPruneFraction does
// not catch it: it is a per-kind fraction, and one delete among N intents is under any threshold.
func TestPinnedVersionCannotBeDeleted(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	base := Declarations{
		Views: []Declaration{{Name: "hosts2", Selector: types.ViewSelector{Kinds: []string{"host"}}}},
		Intents: []types.Intent{{
			Name: "app-d", Kind: types.IntentApplication, Version: 1,
			Spec: map[string]any{"package": "nginx"},
		}},
		Assignments: []types.Assignment{{
			Name: "prod-d", Intent: "app-d", IntentVersion: 1, View: "hosts2",
			Blueprint: "bp-d", BlueprintVersion: 1,
		}},
		Blueprints: []types.Blueprint{{
			Name: "bp-d", Version: 1, For: types.IntentApplication,
			Routes: []types.BlueprintRoute{{
				Observe: types.FacetExpectation{Namespace: "app.config", Path: "port", Equals: []byte(`"443"`)},
				Claim:   types.ClaimExclusive,
			}},
		}},
	}
	if _, err := Apply(ctx, s, base); err != nil {
		t.Fatalf("initial apply: %v", err)
	}
	// The in-place bump: @1 disappears from Git, @2 appears, the Assignment still pins @1.
	bumped := base
	bumped.Intents = []types.Intent{{
		Name: "app-d", Kind: types.IntentApplication, Version: 2,
		Spec: map[string]any{"package": "apache"},
	}}
	plan, err := Apply(ctx, s, bumped)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	var del *PlanEntry
	for i := range plan.Entries {
		if plan.Entries[i].Kind == KindIntent && plan.Entries[i].Action == ActionDelete {
			del = &plan.Entries[i]
		}
	}
	if del == nil || del.Error == "" {
		t.Fatalf("deleting a PINNED version must be refused; got %+v", del)
	}
	if _, err := s.GetIntent(ctx, "app-d", 1); err != nil {
		t.Fatalf("the pinned version must survive a refused delete: %v", err)
	}
}

// TestUnpinnedVersionIsFreelyEditable: the guard must not freeze the estate. A version NO declared
// Assignment pins — a draft, or one whose Assignment was repointed in the same change — is ordinary
// desired state and updates normally. Without this the rule would make iteration impossible.
func TestUnpinnedVersionIsFreelyEditable(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	decls := Declarations{
		Intents: []types.Intent{{
			Name: "draft", Kind: types.IntentApplication, Version: 1,
			Spec: map[string]any{"package": "nginx"},
		}},
	}
	if _, err := Apply(ctx, s, decls); err != nil {
		t.Fatalf("initial apply: %v", err)
	}
	decls.Intents[0].Spec = map[string]any{"package": "apache"}
	plan, err := Apply(ctx, s, decls)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, e := range plan.Entries {
		if e.Kind == KindIntent && e.Error != "" {
			t.Fatalf("an unpinned version must be editable, got refusal: %s", e.Error)
		}
	}
	stored, err := s.GetIntent(ctx, "draft", 1)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Spec["package"] != "apache" {
		t.Fatalf("an unpinned edit must apply, got %v", stored.Spec)
	}
}

// TestProvisioningKindRefusesAVersion is D3, enforced through the DERIVED predicate so a new
// provisioning kind inherits the refusal instead of needing someone to remember a list.
func TestProvisioningKindRefusesAVersion(t *testing.T) {
	for _, kind := range []string{types.IntentCompute, types.IntentSubnet, types.IntentVlan} {
		if types.AssignableIntentKind(kind) {
			t.Errorf("%s must not be assignable/versionable", kind)
		}
		err := ValidateIntent(types.Intent{Name: "x", Kind: kind, Version: 2})
		if err == nil {
			t.Errorf("%s must refuse a version (ADR-0119 D3)", kind)
			continue
		}
		if !strings.Contains(err.Error(), "cannot carry a version") {
			t.Errorf("%s: the refusal must explain itself; got %v", kind, err)
		}
	}
	// Application-shaped kinds are versionable, which is the whole point.
	if !types.AssignableIntentKind(types.IntentApplication) {
		t.Error("Intent/Application must be versionable")
	}
}

// TestGuardPinnedVersionsIsPure covers D6's decision logic WITHOUT a database, because the
// end-to-end tests above t.Skip() when no Postgres is reachable — which means they do not guard
// anything in `task ci`. That gap has bitten this arc three times (the compiler's wiring, the
// schedule Trigger's params, the trigger engine having no tests at all), so the rule gets a pure
// test as well as an integration one.
func TestGuardPinnedVersionsIsPure(t *testing.T) {
	decls := Declarations{Assignments: []types.Assignment{
		{Name: "prod", Intent: "app", IntentVersion: 1, Blueprint: "bp", BlueprintVersion: 2},
		{Name: "stage", Intent: "app", IntentVersion: 2, Blueprint: "bp", BlueprintVersion: 2},
	}}

	entry := func(kind, name string, action Action) PlanEntry {
		return PlanEntry{Kind: kind, Name: name, Action: action}
	}
	plan := &Plan{Entries: []PlanEntry{
		entry(KindIntent, "app@1", ActionUpdate),   // pinned by prod  → refuse
		entry(KindIntent, "app@2", ActionDelete),   // pinned by stage → refuse
		entry(KindIntent, "app@3", ActionUpdate),   // pinned by nobody → allow
		entry(KindIntent, "app@1", ActionCreate),   // create is never a refusal
		entry(KindBlueprint, "bp@2", ActionUpdate), // same rule, other Kind → refuse
		entry(KindBlueprint, "bp@9", ActionDelete), // unpinned → allow
		entry(KindView, "hosts", ActionDelete),     // unrelated Kind → untouched
	}}
	if err := guardPinnedVersions(plan, decls); err != nil {
		t.Fatal(err)
	}
	want := []bool{true, true, false, false, true, false, false} // true = refused
	for i, refused := range want {
		got := plan.Entries[i].Error != ""
		if got != refused {
			t.Errorf("entry %d (%s %s %s): refused=%v, want %v (err=%q)",
				i, plan.Entries[i].Kind, plan.Entries[i].Name, plan.Entries[i].Action, got, refused,
				plan.Entries[i].Error)
		}
	}
	// The message must name every pinning Assignment, since retiring a version means finding them
	// all. Both rings pin bp@2, so both must appear.
	if e := plan.Entries[4].Error; !strings.Contains(e, "prod") || !strings.Contains(e, "stage") {
		t.Errorf("a refusal must name EVERY pinning Assignment; got: %s", e)
	}
	// And it must distinguish the two verbs, because the fix differs.
	if !strings.Contains(plan.Entries[0].Error, "edited in place") {
		t.Errorf("an update refusal should say so; got: %s", plan.Entries[0].Error)
	}
	if !strings.Contains(plan.Entries[1].Error, "removed") {
		t.Errorf("a delete refusal should say so; got: %s", plan.Entries[1].Error)
	}
}

// TestGuardUsesDeclaredAssignmentsNotStored pins a deliberate choice: "pinned" means a DECLARED
// Assignment names it. So removing an Assignment AND the version it pinned in one commit is legal —
// the dependency disappears in the same change as its target. Keying off the STORED set instead
// would make that edit permanently impossible without a two-step dance.
func TestGuardUsesDeclaredAssignmentsNotStored(t *testing.T) {
	plan := &Plan{Entries: []PlanEntry{{Kind: KindIntent, Name: "app@1", Action: ActionDelete}}}
	if err := guardPinnedVersions(plan, Declarations{}); err != nil {
		t.Fatal(err)
	}
	if plan.Entries[0].Error != "" {
		t.Fatalf("with no declared Assignment pinning it, a version must be deletable; got: %s",
			plan.Entries[0].Error)
	}
}

// The KEY half of the Blueprint→Workflow param check, moved to declaration (ADR-0118 D3).
//
// The compiler already validates the substituted params, but Compile() is only driven by a test
// that skips without Postgres — so a mistyped param key in the reference estate passed `task ci`
// and would have surfaced at the first real compile instead. Keys and required-ness need no
// substitution, so they can be checked in Git review; only value TYPES have to wait for the
// resolved spec.
func TestBlueprintParamKeysCheckedAtDeclaration(t *testing.T) {
	const revoke = "name: retire\ninputs:\n  type: object\n  additionalProperties: false\n" +
		"  required: [subject]\n  properties:\n    subject: {type: string}\n    note: {type: string}\n" +
		"steps:\n  - {name: s, viewName: v, actuator: script, params: {script: \"echo {{.launch.subject}}\"}}\n"
	bp := func(removeParams string) string {
		return "name: access\nversion: 1\nfor: Intent/Access\nseverity: warning\n" +
			"removeWorkflow: retire\n" + removeParams +
			"routes: [{observe: {namespace: access.grants, contains: {subject: '{{.spec.subject}}'}}, claim: additive}]\n"
	}
	setup := func(t *testing.T, removeParams string) string {
		t.Helper()
		root := t.TempDir()
		writeDecl(t, root, "v.yaml", "name: v\nselector: {kinds: [host]}\n")
		writeKind(t, root, "workflows", "w.yaml", revoke)
		writeKind(t, root, "blueprints", "b.yaml", bp(removeParams))
		return root
	}

	// Satisfied: parses.
	if _, err := ParseDir(setup(t, "removeParams: {subject: '{{.spec.subject}}'}\n"), nil); err != nil {
		t.Fatalf("removeParams naming a declared input must parse: %v", err)
	}

	// Unknown key.
	err := ParseDir2Err(t, setup(t, "removeParams: {subjekt: '{{.spec.subject}}'}\n"))
	if err == nil {
		t.Fatal("removeParams passing an undeclared input must be rejected at declaration")
	}
	for _, want := range []string{"access@1", "subjekt", "retire", "[note subject]"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name the blueprint, the bad key, the workflow and the valid set; got: %v", err)
		}
	}

	// A required input nobody passes — the withdrawal would be refused at launch, at the one
	// moment an operator is trying to clean up retired state.
	err = ParseDir2Err(t, setup(t, "removeParams: {note: retired}\n"))
	if err == nil {
		t.Fatal("removeParams omitting a required input must be rejected at declaration")
	}
	if !strings.Contains(err.Error(), `requires input "subject"`) {
		t.Errorf("the error must name the missing required input; got: %v", err)
	}
}

// The same check covers a ROUTE's remediationParams, not just removeParams — otherwise the
// earlier failure would be a withdrawal-only privilege while the far more frequently exercised
// remediation path kept waiting for a Postgres-gated compile.
func TestRouteRemediationParamKeysCheckedAtDeclaration(t *testing.T) {
	const apply = "name: converge\ninputs:\n  type: object\n  additionalProperties: false\n" +
		"  properties:\n    subject: {type: string}\n" +
		"steps:\n  - {name: s, viewName: v, actuator: script, params: {script: \"echo {{.launch.subject}}\"}}\n"
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: v\nselector: {kinds: [host]}\n")
	writeKind(t, root, "workflows", "w.yaml", apply)
	writeKind(t, root, "blueprints", "b.yaml",
		"name: access\nversion: 1\nfor: Intent/Access\nseverity: warning\n"+
			"routes: [{observe: {namespace: access.grants, contains: {subject: '{{.spec.subject}}'}}, "+
			"claim: additive, remediationWorkflow: converge, remediationParams: {subjekt: x}}]\n")

	err := ParseDir2Err(t, root)
	if err == nil {
		t.Fatal("a route's remediationParams must be key-checked at declaration too")
	}
	for _, want := range []string{"route 0 remediationParams", "subjekt", "converge"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must locate the route; got: %v", err)
		}
	}
}

// A Blueprint naming a Workflow that is not declared here must be left alone: unresolved refs are
// the compiler's cross-reference check, and two different errors for one mistake is worse than
// one. Without this the loader would start reporting missing Workflows in its own words.
func TestBlueprintNamingAnUndeclaredWorkflowIsLeftToTheCompiler(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: v\nselector: {kinds: [host]}\n")
	writeKind(t, root, "blueprints", "b.yaml",
		"name: access\nversion: 1\nfor: Intent/Access\nseverity: warning\n"+
			"removeWorkflow: nowhere\nremoveParams: {anything: x}\n"+
			"routes: [{observe: {namespace: access.grants, contains: {subject: '{{.spec.subject}}'}}, claim: additive}]\n")

	if _, err := ParseDir(root, nil); err != nil {
		t.Fatalf("an unresolved workflow ref must not be reported by this check: %v", err)
	}
}

// checkProvisioningBuildInputs is the provisioning half of ADR-0118 D3's cross-check (ADR-0120 D3).
// Provisioning has no compile, so this is the earliest point a build Workflow's interface can be
// checked against what the reconcile will actually hand it — and unlike a Blueprint route, the params
// are CORE-generated, so a mismatch is always a Workflow authoring error.
//
// It earned its keep immediately: it caught estate/workflows/vsphere-vm-build.yaml declaring no
// `inputs` at all, which meant the vsphere-dc environment's Compute builder could not be told which
// instance to build.
func TestProvisioningBuildInputsCheckedAtDeclaration(t *testing.T) {
	const intent = "name: web-fleet\nkind: Intent/Compute\nspec:\n" +
		"  count: 2\n  namePrefix: web\n  projectKind: host\n  labels: {fleet: web}\n" +
		"  requires: [provisioning]\n  params: {region: us-east-1}\n"
	// An Actuator advertising a Compute builder is what makes a Workflow a build Workflow.
	const act = "name: awsec2\naddress: stratt-awsec2:9090\npluginIdentity: awsec2\ntier: trusted\n" +
		"provides: [provisioning]\nprovisions: {Compute: compute-build}\n"

	// The step's params are LITERAL, not {{.launch.x}} bindings. That is faithful to the defect:
	// the real vsphere-vm-build hardcoded `name: web-01`, so checkLaunchFields — which only fires on
	// a {{.launch.x}} binding — saw nothing wrong. A Workflow that binds nothing is exactly the one
	// this check has to catch, and a fixture that bound something would be caught by the older check
	// and prove nothing about this one.
	setupWith := func(t *testing.T, wfInputs, script string) string {
		t.Helper()
		root := t.TempDir()
		writeDecl(t, root, "v.yaml", "name: v\nselector: {kinds: [host]}\n")
		writeKind(t, root, "intents", "i.yaml", intent)
		writeKind(t, root, "actuators", "a.yaml", act)
		writeKind(t, root, "workflows", "w.yaml",
			// A gate Step, because a provisioning build is gated (§5 Flow 1) and the check now
			// refuses a builder without one. Not what these cases are about, so it is constant.
			"name: compute-build\n"+wfInputs+
				"steps:\n  - {name: approve, gate: {approvers: {teams: [platform-admins]}, timeoutSeconds: 3600}}\n"+
				"  - {name: s, needs: [approve], viewName: v, actuator: script, params: {script: \""+script+"\"}}\n")
		return root
	}
	// The default step binds the full correlation-critical set.
	setup := func(t *testing.T, wfInputs string) string {
		t.Helper()
		if wfInputs == "" {
			// A Workflow with NO declared inputs cannot bind anything — a binding would trip the
			// namespace check first and prove nothing about the missing interface.
			return setupWith(t, "", "echo nothing")
		}
		return setupWith(t, wfInputs, "echo {{.launch.instance}} {{.launch.projectKind}} {{.launch.labels}}")
	}

	// The full generated set: parses.
	// ADR-0123 D3: a builder declares only what it BINDS, and core sends only what it declares —
	// so the "full generated set" is now the set this fixture's step actually consumes.
	full := "inputs:\n  type: object\n  additionalProperties: false\n  required: [instance]\n  properties:\n" +
		"    instance: {type: string}\n    projectKind: {type: string}\n" +
		"    labels: {type: object, additionalProperties: true}\n"
	if _, err := ParseDir(setup(t, full), nil); err != nil {
		t.Fatalf("a build Workflow declaring the generated set must parse: %v", err)
	}

	// NO inputs at all — the defect this whole ADR is about: the reconcile cannot say which
	// instance, so every instance after the first is unbuildable through the gated path.
	err := ParseDir2Err(t, setup(t, ""))
	if err == nil {
		t.Fatal("a build Workflow with no inputs must be refused")
	}
	// "unreachable" rather than "unbuildable": the same check now covers advertised TEARDOWN
	// Workflows too (ADR-0114 D4), so the consequence is worded for both acts.
	for _, want := range []string{"web-fleet", "compute-build", "declares no `inputs`", "unreachable through the gated path"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name both documents and the consequence; want %q in: %v", want, err)
		}
	}

	// A key it DECLARES but no Step binds: accepted and silently dropped (ADR-0123 D3). This
	// replaces the old inverted case — "a supplied key it fails to declare" — which stopped being a
	// defect once core sends only the declared set. `placement` is the example on purpose: declared
	// by every builder and bound by none is exactly how a declared placement reached no provider.
	dropped := "inputs:\n  type: object\n  additionalProperties: false\n  properties:\n" +
		"    instance: {type: string}\n    projectKind: {type: string}\n" +
		"    labels: {type: object, additionalProperties: true}\n" +
		"    placement: {type: object, additionalProperties: true}\n"
	err = ParseDir2Err(t, setup(t, dropped))
	if err == nil {
		t.Fatal("a build Workflow declaring an input no Step binds must be refused")
	}
	if !strings.Contains(err.Error(), "placement") || !strings.Contains(err.Error(), "no Step binds") {
		t.Errorf("the refusal must name the dropped input: %v", err)
	}

	// And the correlation-critical one: omitting `labels` is INVISIBLE — the build succeeds, the
	// Entity appears, and the Finding never resolves because the correlation label was never
	// projected (the ADR-0120 defect, reachable again now that declaring is optional).
	noLabels := "inputs:\n  type: object\n  additionalProperties: false\n  properties:\n" +
		"    instance: {type: string}\n    projectKind: {type: string}\n"
	err = ParseDir2Err(t, setupWith(t, noLabels, "echo {{.launch.instance}} {{.launch.projectKind}}"))
	if err == nil {
		t.Fatal("a build Workflow that does not declare `labels` must be refused")
	}
	if !strings.Contains(err.Error(), "labels") {
		t.Errorf("the refusal must name the correlation-critical input: %v", err)
	}

	// REQUIRING something the reconcile never sends: every build would be refused at launch. This is
	// the direction a Workflow author gets wrong by copying a hand-launched Workflow.
	extra := full + "  required: [instance, targetSubnet]\n"
	_ = extra // required is declared once above; build the variant explicitly instead
	needsExtra := "inputs:\n  type: object\n  additionalProperties: false\n  required: [instance, targetSubnet]\n  properties:\n" +
		"    instance: {type: string}\n    projectKind: {type: string}\n" +
		"    labels: {type: object, additionalProperties: true}\n    targetSubnet: {type: string}\n"
	err = ParseDir2Err(t, setup(t, needsExtra))
	if err == nil {
		t.Fatal("a build Workflow requiring an input the reconcile never supplies must be refused")
	}
	// Caught by the declared-but-unsuppliable rule (ADR-0123 D3) rather than the required-set one:
	// core now sends only declared inputs, so an input it cannot fill is refused whether or not the
	// Workflow marks it required — strictly earlier, and the same defect.
	if !strings.Contains(err.Error(), "targetSubnet") || !strings.Contains(err.Error(), "never supplies") {
		t.Errorf("the refusal must name the unsatisfiable input: %v", err)
	}
}

// The check must cover EVERY advertised Compute builder, not just whichever one this environment
// happens to bind. Provider selection depends on the active environment and on which providers are
// VERIFIED — runtime state Git cannot see (ADR-0110 D3, ADR-0113 D2) — so a fix that satisfied only
// the bound provider would break the other on a binding change, at exactly the moment nobody is
// looking at the build Workflow.
func TestEveryAdvertisedBuilderIsChecked(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: v\nselector: {kinds: [host]}\n")
	writeKind(t, root, "intents", "i.yaml",
		"name: web-fleet\nkind: Intent/Compute\nspec:\n  count: 1\n  namePrefix: web\n"+
			"  projectKind: host\n  labels: {fleet: web}\n  requires: [provisioning]\n")
	writeKind(t, root, "actuators", "a1.yaml",
		"name: awsec2\naddress: stratt-awsec2:9090\npluginIdentity: awsec2\ntier: trusted\n"+
			"provides: [provisioning]\nprovisions: {Compute: compute-build}\n")
	// A SECOND provider for the same kind — the vcenter/awsec2 situation in the real estate.
	writeKind(t, root, "actuators", "a2.yaml",
		"name: vcenter\naddress: stratt-vcenter:9090\npluginIdentity: vcenter\ntier: trusted\n"+
			"provides: [provisioning]\nprovisions: {Compute: vsphere-vm-build}\n")
	good := "inputs:\n  type: object\n  additionalProperties: false\n  properties:\n" +
		"    instance: {type: string}\n    ordinal: {type: integer}\n    projectKind: {type: string}\n" +
		"    labels: {type: object, additionalProperties: true}\n"
	writeKind(t, root, "workflows", "w1.yaml", "name: compute-build\n"+good+
		"steps:\n  - {name: approve, gate: {approvers: {teams: [platform-admins]}, timeoutSeconds: 3600}}\n"+
		"  - {name: s, needs: [approve], viewName: v, actuator: script, params: {script: \"echo {{.launch.instance}} {{.launch.projectKind}} {{.launch.labels}}\"}}\n")
	// The second builder is NOT parameterized — must still be caught.
	writeKind(t, root, "workflows", "w2.yaml", "name: vsphere-vm-build\n"+
		"steps:\n  - {name: s, viewName: v, actuator: script, params: {script: \"echo {{.launch.instance}} {{.launch.projectKind}} {{.launch.labels}}\"}}\n")

	err := ParseDir2Err(t, root)
	if err == nil {
		t.Fatal("a second advertised builder must be checked too")
	}
	if !strings.Contains(err.Error(), "vsphere-vm-build") {
		t.Fatalf("the refusal must name the unfixed builder, not stop at the first: %v", err)
	}
}

// Singleton builders are checked with the SINGLETON param set, which differs from a fleet's for a
// real reason: no ordinal, and a per-kind (intentKind, name) correlation key so a Subnet named
// "web-dmz" can never collide with a Compute instance of that name (§2). A check that applied the
// fleet set here would demand `instance`/`ordinal` from a Workflow that will never receive them.
func TestSingletonBuildInputsCheckedAtDeclaration(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: v\nselector: {kinds: [host]}\n")
	writeKind(t, root, "intents", "i.yaml",
		"name: app-subnet\nkind: Intent/Subnet\nspec:\n  projectKind: subnet\n"+
			"  requires: [provisioning]\n  params: {cidr: 10.0.1.0/24}\n")
	writeKind(t, root, "actuators", "a.yaml",
		"name: crossplane\naddress: stratt-crossplane:9090\npluginIdentity: crossplane\ntier: trusted\n"+
			"provides: [provisioning]\nprovisions: {Subnet: subnet-build}\n")
	writeKind(t, root, "workflows", "w.yaml",
		// Binds nothing: a Workflow with no declared inputs cannot bind anything, and a binding
		// would trip the namespace check first — proving nothing about the missing interface.
		"name: subnet-build\nsteps:\n  - {name: s, viewName: v, actuator: script, params: {script: echo}}\n")

	err := ParseDir2Err(t, root)
	if err == nil {
		t.Fatal("a singleton builder with no inputs must be refused")
	}
	// The SINGLETON set, not the fleet set: `singleton` and `name`, and NOT instance/ordinal.
	for _, want := range []string{"app-subnet", "subnet-build", "name"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("want %q in: %v", want, err)
		}
	}
	for _, unwanted := range []string{"ordinal", "instance "} {
		if strings.Contains(err.Error(), unwanted) {
			t.Errorf("the fleet param %q must not be demanded of a singleton builder: %v", unwanted, err)
		}
	}
}

// singletonBuilder is a fully-parameterized Intent/Subnet builder whose step params can be varied to
// plant one literal. Everything the reconcile sends is declared, so the input checks pass and the
// identity check is what these tests actually exercise.
//
// The step uses the `mcp` Actuator rather than `script` because the defect being guarded lives INSIDE
// an opaque provider blob, and every core Actuator Contract is additionalProperties:false — a `script`
// step cannot legally carry a nested object at all. mcp's `arguments` is a free-form object, so it
// stands in for crossplane/provision's `spec.forProvider.manifest` and keeps the test faithful to the
// depth the real literal hid at.
func singletonBuilder(args string) string {
	return "name: subnet-build\ninputs:\n  type: object\n  additionalProperties: false\n  properties:\n" +
		"    name: {type: string}\n" +
		"    projectKind: {type: string}\n    labels: {type: object, additionalProperties: true}\n" +
		"    params: {type: object, additionalProperties: true}\n" +
		// The step BINDS every declared input (required since ADR-0123 D3) and carries the varied
		// `args` alongside, so a planted literal is the only thing these cases change.
		"steps:\n  - {name: approve, gate: {approvers: {teams: [platform-admins]}, timeoutSeconds: 3600}}\n" +
		"  - {name: s, needs: [approve], viewName: v, actuator: mcp, params: {server: prov, arguments: " +
		"{name: \"{{.launch.name}}\", kind: \"{{.launch.projectKind}}\", labels: \"{{.launch.labels}}\", opaque: \"{{.launch.params}}\", planted: " + args + "}}}\n"
}

func singletonEstate(t *testing.T, stepParams string) string {
	t.Helper()
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: v\nselector: {kinds: [host]}\n")
	writeKind(t, root, "intents", "i.yaml",
		"name: app-subnet\nkind: Intent/Subnet\nspec:\n  projectKind: subnet\n"+
			"  requires: [provisioning]\n  params: {cidr: 10.30.0.0/24}\n")
	writeKind(t, root, "actuators", "a.yaml",
		"name: crossplane\naddress: stratt-crossplane:9090\npluginIdentity: crossplane\ntier: trusted\n"+
			"provides: [provisioning]\nprovisions: {Subnet: subnet-build}\n")
	writeKind(t, root, "workflows", "w.yaml", singletonBuilder(stepParams))
	return root
}

// An advertised build Workflow must carry a human approval gate: a provisioning build is GATED, never
// auto-run (§5 Flow 1). Every builder in the reference estate already has one, which is precisely the
// reason to pin it — the invariant held by convention, and this check has repeatedly found conventions
// broken. A builder reaching an operator without a gate turns "launch this build" into "this build has
// happened", with no approval anywhere on the path to real infrastructure.
func TestAdvertisedBuilderMustBeGated(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: v\nselector: {kinds: [host]}\n")
	writeKind(t, root, "intents", "i.yaml",
		"name: app-subnet\nkind: Intent/Subnet\nspec:\n  projectKind: subnet\n"+
			"  requires: [provisioning]\n  params: {cidr: 10.30.0.0/24}\n")
	writeKind(t, root, "actuators", "a.yaml",
		"name: crossplane\naddress: stratt-crossplane:9090\npluginIdentity: crossplane\ntier: trusted\n"+
			"provides: [provisioning]\nprovisions: {Subnet: subnet-build}\n")
	// Fully parameterized and correctly typed — the ONLY thing wrong is the missing gate, so a pass
	// here could not be explained by any other check.
	writeKind(t, root, "workflows", "w.yaml",
		"name: subnet-build\ninputs:\n  type: object\n  additionalProperties: false\n  properties:\n"+
			"    name: {type: string}\n"+
			"    projectKind: {type: string}\n    labels: {type: object, additionalProperties: true}\n"+
			"steps:\n  - {name: s, viewName: v, actuator: mcp, params: {server: prov, "+
			"arguments: {name: \"{{.launch.name}}\", kind: \"{{.launch.projectKind}}\", "+
			"labels: \"{{.launch.labels}}\"}}}\n")

	err := ParseDir2Err(t, root)
	if err == nil {
		t.Fatal("an ungated provisioning builder must be refused (§5 Flow 1)")
	}
	for _, want := range []string{"subnet-build", "no approval gate", "GATED", "never auto-run"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("want %q in: %v", want, err)
		}
	}
}

// A builder must not name a declaration of the kind it builds, AT ANY DEPTH — including inside the
// opaque provider blob, which is where the real one hid.
//
// estate/workflows/subnet-build.yaml bound {{.launch.name}} in its step params while its nested
// spec.forProvider.manifest still said `subnet-app-subnet`. app-subnet and dmz-subnet are both
// Intent/Subnet and both route to that one Workflow, so building dmz-subnet would have applied a
// ConfigMap under APP-SUBNET's name — an overwrite of the other subnet's resource, not a failure any
// operator would see. The top-level binding made it look parameterized.
func TestBuilderMustNotNameOneDeclarationItBuilds(t *testing.T) {
	root := singletonEstate(t, "{spec: {forProvider: {manifest: {metadata: {name: subnet-app-subnet}}}}}")

	err := ParseDir2Err(t, root)
	if err == nil {
		t.Fatal("a builder that hardcodes one declaration's name must be refused, however deeply nested")
	}
	for _, want := range []string{"subnet-app-subnet", "app-subnet", "{{.launch.name}}", "identity-BLIND"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("want %q in: %v", want, err)
		}
	}
}

// The other half: a builder must not carry a declaration's opaque `params` VALUE either. Getting the
// name right and the config wrong is the quieter failure — the resource lands under the correct name
// with the wrong CIDR, so the estate looks built and is misconfigured.
func TestBuilderMustNotCarryOneDeclarationsParamValue(t *testing.T) {
	root := singletonEstate(t, "{spec: {forProvider: {manifest: {data: {cidr: 10.30.0.0/24}}}}}")

	err := ParseDir2Err(t, root)
	if err == nil {
		t.Fatal("a builder that hardcodes one declaration's declared param value must be refused")
	}
	for _, want := range []string{"10.30.0.0/24", "params.cidr", "{{.launch.params.cidr}}"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("want %q in: %v", want, err)
		}
	}
}

// The check must stay QUIET on a correctly-parameterized builder, and — the part that matters for
// false positives — on literals belonging to a DIFFERENT kind. subnet-build legitimately targets the
// Intent/Vlan "net-vlan" as its in-vlan edge; comparing a Subnet builder against Vlan declarations
// would refuse a correct estate and push authors to silence the check.
func TestBuilderMayNameADeclarationOfAnotherKind(t *testing.T) {
	root := singletonEstate(t,
		"{name: \"{{.launch.name}}\", cidr: \"{{.launch.params.cidr}}\", "+
			"relations: [{type: in-vlan, toScheme: crossplane.claim, toValue: net-vlan}]}")
	writeKind(t, root, "intents", "i2.yaml",
		"name: net-vlan\nkind: Intent/Vlan\nspec:\n  projectKind: vlan\n"+
			"  requires: [provisioning]\n  params: {vid: \"100\"}\n")
	// net-vlan needs its own builder, or the dangling/unbuildable checks fire for an unrelated reason.
	writeKind(t, root, "actuators", "a.yaml",
		"name: crossplane\naddress: stratt-crossplane:9090\npluginIdentity: crossplane\ntier: trusted\n"+
			"provides: [provisioning]\nprovisions: {Subnet: subnet-build, Vlan: vlan-build}\n")
	writeKind(t, root, "workflows", "w2.yaml",
		strings.Replace(singletonBuilder("{vid: \"{{.launch.params.vid}}\"}"),
			"name: subnet-build", "name: vlan-build", 1))

	if err := ParseDir2Err(t, root); err != nil {
		t.Fatalf("a correctly-parameterized builder naming another KIND's declaration must pass: %v", err)
	}
}

// A provisions map naming a Workflow that does not exist must be refused at declaration. Nothing
// caught this before: validateProvisions only checks the entry is non-empty, and the reconcile copies
// the name onto the build Finding — so the reference estate advertised `Subnet: opentofu-subnet-build`
// against a Workflow that was never written, and a declared Intent/Subnet produced a Finding offering
// a Workflow nobody could launch. Subnet provisioning was dead and the suite was green.
func TestDanglingProvisionsTargetIsRefused(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: v\nselector: {kinds: [host]}\n")
	writeKind(t, root, "actuators", "a.yaml",
		"name: opentofu-network\naddress: stratt-opentofu:9090\npluginIdentity: opentofu\ntier: trusted\n"+
			"provides: [provisioning]\nprovisions: {Subnet: opentofu-subnet-build}\n")

	err := ParseDir2Err(t, root)
	if err == nil {
		t.Fatal("advertising a build Workflow that does not exist must be refused")
	}
	for _, want := range []string{"opentofu-subnet-build", "Subnet", "no such", "cannot keep"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name the phantom Workflow and the promise; want %q in: %v", want, err)
		}
	}
}
