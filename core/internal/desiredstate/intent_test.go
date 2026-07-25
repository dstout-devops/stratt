package desiredstate

import (
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
intent: chrome
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
		"bad blueprint ref":   {"assignments": "name: a\nintent: i\nview: v\nblueprint: application\n"},
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
		"name: prod-web\nintent: web\nview: hosts\nblueprint: web-server@1\nenvironments: [prod]\n"+
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
		"name: web\nintent: web\nview: hosts\nblueprint: web-server@1\nenvironments: [prod, staging]\n"+
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
		"name: web\nintent: web\nview: hosts\nblueprint: web-server@1\nenvironments: [prod]\n"+
			"values: {staging: something}\n")
	if _, err := ParseDir(root, nil); err != nil {
		t.Fatalf("a value key naming an environment this Assignment does not declare is not detectable here: %v", err)
	}
}
