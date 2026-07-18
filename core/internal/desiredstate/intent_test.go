package desiredstate

import (
	"os"
	"path/filepath"
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
		"unimplemented kind": {"intents": "name: x\nkind: Intent/Config\nspec: {}\n"},      // charter-named, no schema yet
		"invalid cert spec":  {"intents": "name: x\nkind: Intent/Certificate\nspec: {}\n"}, // missing required issuer/commonName/renewBefore
		"remove on non-cert": {"intents": "name: x\nkind: Intent/Application\nonRemove: remove\n"},
		"revert on non-file": {"intents": "name: x\nkind: Intent/Application\nonRemove: revert\n"}, // Application supports neither
		"blueprint no ver":   {"blueprints": "name: b\nfor: Intent/Application\nroutes: [{observe: {namespace: n, equals: 1}, claim: additive}]\n"},
		"blueprint bad kind": {"blueprints": "name: b\nversion: 1\nfor: Intent/Config\nroutes: [{observe: {namespace: n, equals: 1}, claim: additive}]\n"},
		"bad claim":          {"blueprints": "name: b\nversion: 1\nfor: Intent/Application\nroutes: [{observe: {namespace: n, equals: 1}, claim: priority}]\n"},
		"bad blueprint ref":  {"assignments": "name: a\nintent: i\nview: v\nblueprint: application\n"},
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
			"spec: {issuer: certissuer/stratt-dev, commonName: web.stratt.test, renewBefore: 360h, exportable: false}\n")
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
