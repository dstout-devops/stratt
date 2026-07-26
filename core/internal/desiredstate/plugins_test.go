package desiredstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Admitted plugin estates (ADR-0137 D1/D3): a plugin owns its declarations, the
// estate decides whether to run them.

// admitting writes an estate that admits one plugin estate, and returns both roots.
func admitting(t *testing.T, admission string) (estate, plugin string) {
	t.Helper()
	root := t.TempDir()
	estate, plugin = filepath.Join(root, "estate"), filepath.Join(root, "plug")
	for _, d := range []string{estate, plugin} {
		if err := os.MkdirAll(filepath.Join(d, "views"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// views/ is required of the ESTATE; the plugin's is created here only so tests
	// can put declarations in it.
	writeDecl(t, estate, "hosts.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	if err := os.WriteFile(filepath.Join(estate, "plugins.yaml"), []byte(admission), 0o644); err != nil {
		t.Fatal(err)
	}
	return estate, plugin
}

// TestAdmittedPluginDeclarationsLoad is the mechanism's whole point: declarations
// living in the plugin's tree reach the estate that admitted them.
func TestAdmittedPluginDeclarationsLoad(t *testing.T) {
	estate, plugin := admitting(t, "plugins:\n  - name: p\n    path: ../plug\n")
	writeKind(t, plugin, "actuators", "a.yaml",
		"name: tool\npluginIdentity: ansible\njobCommand: [stratt-ansible]\n")
	writeKind(t, plugin, "workflows", "w.yaml",
		"name: converge\nsteps:\n  - name: go\n    viewName: hosts\n    actuator: tool\n    params: {}\n")

	decls, err := ParseDir(estate, nil)
	if err != nil {
		t.Fatalf("an admitted plugin's declarations must load: %v", err)
	}
	if len(decls.Actuators) != 1 || decls.Actuators[0].Name != "tool" {
		t.Errorf("actuators: %+v", decls.Actuators)
	}
	if len(decls.Workflows) != 1 || decls.Workflows[0].Name != "converge" {
		t.Errorf("workflows: %+v", decls.Workflows)
	}
}

// TestUnadmittedPluginIsNotLoaded is the AUTHORITY half, and the more important
// one: locality is not authority (ADR-0137 D3). A declaration sitting beside the
// estate that nothing admitted must not run — an Actuator declaration carries a
// facetNamespaces write ceiling, so a plugin that could install itself would be a
// vendor granting itself authority.
func TestUnadmittedPluginIsNotLoaded(t *testing.T) {
	root := t.TempDir()
	estate := filepath.Join(root, "estate")
	if err := os.MkdirAll(filepath.Join(estate, "views"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDecl(t, estate, "hosts.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	// A complete plugin estate, sitting right there, admitted by nothing.
	plugin := filepath.Join(root, "plug")
	writeKind(t, plugin, "actuators", "a.yaml",
		"name: uninvited\npluginIdentity: ansible\njobCommand: [stratt-ansible]\nfacetNamespaces: [os.kernel]\n")

	decls, err := ParseDir(estate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(decls.Actuators) != 0 {
		t.Fatalf("an unadmitted plugin must contribute nothing, got %+v", decls.Actuators)
	}
}

// TestMissingPluginsFileIsNotAnError: an estate that admits no plugins is a normal
// estate, and every estate predating this mechanism is one.
func TestMissingPluginsFileIsNotAnError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "views"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeDecl(t, root, "hosts.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	if _, err := ParseDir(root, nil); err != nil {
		t.Fatalf("no plugins.yaml must mean 'admits nothing', not a failure: %v", err)
	}
}

// TestAdmittedPathMustExist: the difference between "declared nothing" and
// "declared something unreadable". A skipped typo would read as a working estate
// running none of that plugin's Workflows (§1.8).
func TestAdmittedPathMustExist(t *testing.T) {
	estate, _ := admitting(t, "plugins:\n  - name: p\n    path: ../typo\n")
	_, err := ParseDir(estate, nil)
	if err == nil || !strings.Contains(err.Error(), "typo") {
		t.Fatalf("an admission pointing nowhere must fail the load and name the path: %v", err)
	}
}

// TestAdmissionTyposFail: KnownFields on the admission list. A misspelled key that
// parsed to an empty list would admit nothing, silently.
func TestAdmissionTyposFail(t *testing.T) {
	estate, _ := admitting(t, "plugins:\n  - name: p\n    pathh: ../plug\n")
	if _, err := ParseDir(estate, nil); err == nil {
		t.Fatal("a typo in plugins.yaml must fail the load rather than admit nothing")
	}
}

// TestDuplicateNameAcrossEstates is the §2.4 rule the merge must not break: two
// declarations of the same name is a hard error naming BOTH files. A merge order
// that let one shadow the other would be implicit precedence — and which one won
// would depend on load order, the thing this project refuses everywhere else.
func TestDuplicateNameAcrossEstates(t *testing.T) {
	estate, plugin := admitting(t, "plugins:\n  - name: p\n    path: ../plug\n")
	writeKind(t, estate, "workflows", "w.yaml",
		"name: converge\nsteps:\n  - name: go\n    viewName: hosts\n    actuator: tool\n    params: {}\n")
	writeKind(t, plugin, "actuators", "a.yaml",
		"name: tool\npluginIdentity: ansible\njobCommand: [stratt-ansible]\n")
	writeKind(t, plugin, "workflows", "w.yaml",
		"name: converge\nsteps:\n  - name: go\n    viewName: hosts\n    actuator: tool\n    params: {}\n")

	_, err := ParseDir(estate, nil)
	if err == nil {
		t.Fatal("the same Workflow name in two estates must be refused, never silently resolved")
	}
	if !strings.Contains(err.Error(), "declared in both") {
		t.Fatalf("the diagnostic must name both files: %v", err)
	}
}

// TestPluginContentResolvesAgainstItsOwnEstate is the trap ADR-0137 D1 creates and
// this is where it gets caught: `contentDir` is relative to the estate that
// SHIPPED the Actuator, not the one that admitted it. Resolving against the
// admitting root would send `contentDir: content/x` hunting under estate/, and the
// plugin's own tree would be unreachable the moment it moved.
func TestPluginContentResolvesAgainstItsOwnEstate(t *testing.T) {
	estate, plugin := admitting(t, "plugins:\n  - name: p\n    path: ../plug\n")
	writeKind(t, plugin, "actuators", "a.yaml",
		"name: tool\npluginIdentity: ansible\njobCommand: [stratt-ansible]\ncontentDir: content/proj\ncontentInputs: [entry]\n")
	if err := os.MkdirAll(filepath.Join(plugin, "content", "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugin, "content", "proj", "site.yml"), []byte("- hosts: all\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A decoy at the SAME relative path under the admitting estate. If resolution
	// ever regresses to the estate root this test still passes on file count — so
	// the content is asserted too.
	if err := os.MkdirAll(filepath.Join(estate, "content", "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(estate, "content", "proj", "site.yml"), []byte("- hosts: WRONG\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	decls, err := ParseDir(estate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(decls.Actuators) != 1 {
		t.Fatalf("actuators: %+v", decls.Actuators)
	}
	got := decls.Actuators[0].Content["site.yml"]
	if got != "- hosts: all\n" {
		t.Fatalf("contentDir resolved against the wrong estate root: %q", got)
	}
}

// TestStagedEstateParses guards the OTHER half of the mechanism — the vendoring
// `task dev:stage-estate` does. The staging is shell, it rewrites plugins.yaml to
// local paths, and nothing else would notice if it broke: the estate would render,
// deploy, and the daemon would fail to boot in a cluster. This parses the staged
// tree exactly as the daemon does.
//
// Skipped when nothing is staged (the tree is git-ignored, so a fresh checkout has
// none) rather than failing — a test that demands a build artifact fails for
// everyone who has not run the build.
func TestStagedEstateParses(t *testing.T) {
	staged := filepath.Join("..", "..", "..", "deploy", "charts", "stratt", "dev", "declarations")
	if _, err := os.Stat(filepath.Join(staged, "plugins.yaml")); err != nil {
		t.Skipf("no staged estate (run `task dev:stage-estate`): %v", err)
	}
	decls, err := ParseDir(staged, nil)
	if err != nil {
		t.Fatalf("the STAGED estate must parse exactly as the daemon reads it: %v", err)
	}
	// And the vendored plugin actually arrived — a staging step that copied
	// plugins.yaml but not the estates would parse only if it also failed, so
	// assert presence rather than trusting the absence of an error.
	var found bool
	for _, a := range decls.Actuators {
		if a.Name == "ansible-platform-baseline" && len(a.Content) > 0 {
			found = true
		}
	}
	if !found {
		t.Error("the staged tree must carry the vendored plugin estate, content and all")
	}
}
