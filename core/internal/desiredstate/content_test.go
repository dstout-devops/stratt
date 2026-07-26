package desiredstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tool content beside the estate (ADR-0134). These pin the load-time half of the decision:
// what an Actuator's declared content root resolves to, and the ways a bad one is refused
// HERE rather than in a pod at 3 a.m. (§1.8).

// actuatorWithContent writes the smallest estate that exercises a contentDir: a View (the
// only non-optional kind), an EE-Job Actuator declaring `dir`, and a Workflow whose Step
// names `playbook`. Returns the root.
func actuatorWithContent(t *testing.T, dir, playbook string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	writeKind(t, root, "actuators", "a.yaml",
		"name: tool\npluginIdentity: ansible\njobCommand: [stratt-ansible]\n"+
			"contentDir: "+dir+"\ncontentInputs: [playbook]\n")
	step := "  - name: run\n    viewName: hosts\n    actuator: tool\n"
	if playbook != "" {
		step += "    params:\n      playbook: " + playbook + "\n"
	}
	writeKind(t, root, "workflows", "w.yaml", "name: w\nsteps:\n"+step)
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestContentDirResolvesOntoTheActuator is D3's core claim: the tree is read at PARSE time
// and carried on the Actuator, keyed by path relative to the content root. Nothing at
// dispatch reads a filesystem, which is what lets the content travel to a remote Site and
// what puts a playbook edit into `stratt plan`'s diff.
func TestContentDirResolvesOntoTheActuator(t *testing.T) {
	root := actuatorWithContent(t, "ansible/projects/p", "site.yml", map[string]string{
		"ansible/projects/p/site.yml":              "- hosts: all\n",
		"ansible/projects/p/roles/common/main.yml": "- debug: {msg: hi}\n",
	})
	decls, err := ParseDir(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(decls.Actuators) != 1 {
		t.Fatalf("actuators: %d", len(decls.Actuators))
	}
	got := decls.Actuators[0].Content
	if len(got) != 2 {
		t.Fatalf("content: %v", sortedContentPaths(got))
	}
	// A WHOLE DIRECTORY, not one file (D2): a real Ansible project has roles/ and
	// group_vars/, so content authored as Ansible has to keep working as Ansible.
	if got["site.yml"] != "- hosts: all\n" || got["roles/common/main.yml"] == "" {
		t.Fatalf("content keys are not relative to the content root: %v", sortedContentPaths(got))
	}
}

// TestPlaybookReferenceMustResolve is D5's parse-time check, and its boundary. Core asks
// whether a PATH is in the map; it never opens the file and never parses a play
// (ADR-0117 D6). A missing playbook fails the load, not a Run.
func TestPlaybookReferenceMustResolve(t *testing.T) {
	root := actuatorWithContent(t, "ansible/projects/p", "nope.yml", map[string]string{
		"ansible/projects/p/site.yml": "- hosts: all\n",
	})
	_, err := ParseDir(root, nil)
	if err == nil {
		t.Fatal("a Step naming a playbook that does not exist must fail the load")
	}
	// The message has to name what IS there, or the failure starts an investigation
	// instead of ending one (§1.8).
	if !strings.Contains(err.Error(), "nope.yml") || !strings.Contains(err.Error(), "site.yml") {
		t.Fatalf("diagnostic names neither the missing path nor the available ones: %v", err)
	}
}

// TestContentRefIsCheckedByDeclarationNotByName is the §1.4 assertion, and it is the one to
// break if someone reintroduces `params["playbook"]` into the loader.
//
// The reference check finds a bad playbook ONLY because the Actuator declared
// `contentInputs: [playbook]`. Strip that declaration and the identical Step parses clean:
// core has no opinion about a param it was never pointed at. That is not a gap to patch by
// hardcoding the key — it is the same content-blind shape as elevatedInputs, and the price of
// a spine that copies a declared directory without knowing what Ansible is.
func TestContentRefIsCheckedByDeclarationNotByName(t *testing.T) {
	estate := func(t *testing.T, contentInputs string) string {
		t.Helper()
		root := t.TempDir()
		writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
		writeKind(t, root, "actuators", "a.yaml",
			"name: tool\npluginIdentity: ansible\njobCommand: [stratt-ansible]\n"+
				"contentDir: ansible/projects/p\n"+contentInputs)
		writeKind(t, root, "workflows", "w.yaml",
			"name: w\nsteps:\n  - name: run\n    viewName: hosts\n    actuator: tool\n    params:\n      playbook: absent.yml\n")
		dir := filepath.Join(root, "ansible/projects/p")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "site.yml"), []byte("- hosts: all\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return root
	}
	if _, err := ParseDir(estate(t, "contentInputs: [playbook]\n"), nil); err == nil {
		t.Fatal("a declared content input must be checked against the resolved tree")
	}
	if _, err := ParseDir(estate(t, ""), nil); err != nil {
		t.Fatalf("core must have no opinion about an undeclared param key — that is what keeps it tool-blind: %v", err)
	}
}

// TestContentInputsRequireContentDir: a path resolved inside a content root, on an Actuator
// that mounts none, is a reference checked against nothing — admitted and reported nowhere.
func TestContentInputsRequireContentDir(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	writeKind(t, root, "actuators", "a.yaml",
		"name: tool\npluginIdentity: ansible\njobCommand: [stratt-ansible]\ncontentInputs: [playbook]\n")
	_, err := ParseDir(root, nil)
	if err == nil || !strings.Contains(err.Error(), "contentInputs requires contentDir") {
		t.Fatalf("want a contentInputs diagnostic, got %v", err)
	}
}

// TestContentDirRefusals pins the shapes that fail LOUDLY at load rather than quietly in a
// pod. Each is a different layer failing to explain itself: a missing directory reads as an
// empty mount, an escaping path reads a tree the estate does not own, a symlink puts
// unreviewed bytes into an execution pod, an illegal segment cannot be a ConfigMap key, and
// play.yml collides with the name the shim writes an inline play to.
func TestContentDirRefusals(t *testing.T) {
	cases := []struct {
		name  string
		dir   string
		files map[string]string
		want  string
	}{
		{
			name: "missing directory",
			dir:  "ansible/projects/absent",
			// Another project exists, so the estate is otherwise well-formed.
			files: map[string]string{"ansible/projects/p/site.yml": "- hosts: all\n"},
			want:  "contentDir",
		},
		{
			name:  "escapes the estate root",
			dir:   "../elsewhere",
			files: map[string]string{"ansible/projects/p/site.yml": "- hosts: all\n"},
			want:  "illegal path segment",
		},
		{
			name:  "absolute path",
			dir:   "/etc",
			files: map[string]string{"ansible/projects/p/site.yml": "- hosts: all\n"},
			want:  "must be relative",
		},
		{
			name:  "reserved play.yml",
			dir:   "ansible/projects/p",
			files: map[string]string{"ansible/projects/p/play.yml": "- hosts: all\n"},
			want:  "reserved name",
		},
		{
			name:  "empty content root",
			dir:   "ansible/projects/p",
			files: map[string]string{"ansible/projects/other/site.yml": "- hosts: all\n"},
			want:  "contentDir",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// No playbook on the Step: these are declaration-level refusals, and pairing
			// them with a reference would let a reference error mask a content one.
			root := actuatorWithContent(t, tc.dir, "", tc.files)
			if tc.name == "empty content root" {
				if err := os.MkdirAll(filepath.Join(root, "ansible/projects/p"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			_, err := ParseDir(root, nil)
			if err == nil {
				t.Fatal("want a refusal, got a clean parse")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("diagnostic %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

// TestContentDirSymlinkRefused is split out because it needs a real symlink on disk. A link
// inside a reviewed estate would let os.ReadFile pull an unreviewed file into a ConfigMap
// mounted in an execution pod, while a reviewer reads a one-line file and not its target.
func TestContentDirSymlinkRefused(t *testing.T) {
	root := actuatorWithContent(t, "ansible/projects/p", "", map[string]string{
		"ansible/projects/p/site.yml": "- hosts: all\n",
		"secret.txt":                  "not reviewed as content\n",
	})
	if err := os.Symlink(filepath.Join(root, "secret.txt"), filepath.Join(root, "ansible/projects/p/leak.yml")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := ParseDir(root, nil)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("want a symlink refusal, got %v", err)
	}
}

// TestContentDirRequiresJobCommand: only the EE-Job transport mounts content, so a gRPC
// Actuator declaring a contentDir would be admitted, ignored, and reported nowhere — the
// same half-declaration defect ValidateActuator already refuses for facet grants.
func TestContentDirRequiresJobCommand(t *testing.T) {
	root := t.TempDir()
	writeDecl(t, root, "v.yaml", "name: hosts\nselector: {kinds: [host]}\n")
	writeKind(t, root, "actuators", "a.yaml",
		"name: tool\npluginIdentity: p\naddress: localhost:9000\ncontentDir: ansible/projects/p\n")
	_, err := ParseDir(root, nil)
	if err == nil || !strings.Contains(err.Error(), "requires jobCommand") {
		t.Fatalf("want a jobCommand diagnostic, got %v", err)
	}
}

// TestContentDirCeiling: the mounted tree becomes a ConfigMap and K8s caps one at 1 MiB, so
// an oversized root is refused with its size named rather than producing a Job that fails to
// schedule for reasons nobody can see.
func TestContentDirCeiling(t *testing.T) {
	big := strings.Repeat("x", maxContentBytes+1)
	root := actuatorWithContent(t, "ansible/projects/p", "", map[string]string{
		"ansible/projects/p/site.yml": big,
	})
	_, err := ParseDir(root, nil)
	if err == nil || !strings.Contains(err.Error(), "ceiling") {
		t.Fatalf("want a size-ceiling refusal, got %v", err)
	}
}

// TestReferenceEstateProjectsAreScoped is the D2 payoff, asserted over the estate a reader
// copies: each project Actuator's facetNamespaces is bounded to its own remediation domain.
// Grouping projects by domain rather than by tool is what makes a per-project write ceiling
// mean anything — an access project has no business writing hardening facets — so a future
// widening has to argue with this test.
func TestReferenceEstateProjectsAreScoped(t *testing.T) {
	decls, err := ParseDir(estateRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"ansible-access-control": {"access.grants"},
		"ansible-fileset":        {"fileset.content"},
	}
	seen := map[string]bool{}
	for _, a := range decls.Actuators {
		if a.ContentDir == "" {
			continue
		}
		seen[a.Name] = true
		if len(a.Content) == 0 {
			t.Errorf("actuator %q declares contentDir %s but resolved no content", a.Name, a.ContentDir)
		}
		exact, checked := want[a.Name]
		if !checked {
			continue
		}
		if strings.Join(a.FacetNamespaces, ",") != strings.Join(exact, ",") {
			t.Errorf("actuator %q grant is %v, want exactly %v — a per-project ceiling that widens is not a ceiling", a.Name, a.FacetNamespaces, exact)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("actuator %q is missing from the reference estate", name)
		}
	}
}
