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
	parsed, err := ParseDir(root)
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
		"bad intent kind":   {"intents": "name: x\nkind: Intent/Certificate\n"},
		"revert onRemove":   {"intents": "name: x\nkind: Intent/Application\nonRemove: revert\n"},
		"blueprint no ver":  {"blueprints": "name: b\nfor: Intent/Application\nroutes: [{observe: {namespace: n, equals: 1}, claim: additive}]\n"},
		"bad claim":         {"blueprints": "name: b\nversion: 1\nfor: Intent/Application\nroutes: [{observe: {namespace: n, equals: 1}, claim: priority}]\n"},
		"bad blueprint ref": {"assignments": "name: a\nintent: i\nview: v\nblueprint: application\n"},
	} {
		bad := t.TempDir()
		writeDecl(t, bad, "v.yaml", "name: v\nselector: {kinds: [vm]}\n")
		for kind, doc := range docs {
			writeKind(t, bad, kind, "x.yaml", doc)
		}
		if _, err := ParseDir(bad); err == nil {
			t.Fatalf("invalid intent-layer (%s) must be rejected", name)
		}
	}
}

func TestBlueprintVersionsCoexist(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: v\nselector: {kinds: [vm]}\n")
	base := "name: application\nfor: Intent/Application\nroutes: [{observe: {namespace: n, equals: 1}, claim: additive}]\nversion: "
	writeKind(t, root, "blueprints", "v1.yaml", base+"1\n")
	writeKind(t, root, "blueprints", "v2.yaml", base+"2\n")
	parsed, err := ParseDir(root)
	if err != nil {
		t.Fatalf("two versions of one blueprint must coexist: %v", err)
	}
	if len(parsed.Blueprints) != 2 {
		t.Fatalf("blueprints: %+v", parsed.Blueprints)
	}
}
