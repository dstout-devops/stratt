package desiredstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOneDeclarationPerFile(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		refused bool
	}{
		{
			name: "the ordinary shape",
			body: "name: web-hosts\nselector:\n  kinds: [host]\n",
		}, {
			// A leading `---` is punctuation, not a second document. Refusing it would fail a
			// large fraction of hand-written YAML for no reason.
			name: "a leading document marker",
			body: "---\nname: web-hosts\nselector:\n  kinds: [host]\n",
		}, {
			// THE MEASURED DEFECT. Two Views in one file: the first loaded, the second was read
			// by nothing, and it only surfaced because a Workflow referenced the missing one.
			name:    "two declarations in one file",
			body:    "name: secure-hosts\nselector:\n  kinds: [host]\n---\nname: web-hosts\nselector:\n  kinds: [host]\n",
			refused: true,
		}, {
			// Trailing punctuation with nothing behind it drops no declaration, so it is fine.
			name: "a trailing document marker",
			body: "name: web-hosts\nselector:\n  kinds: [host]\n---\n",
		}, {
			// Same, with a comment after the marker: still no declaration lost.
			name: "a trailing marker followed only by comments",
			body: "name: web-hosts\nselector:\n  kinds: [host]\n---\n# a note\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := refuseMultiDocument("views/x.yaml", []byte(c.body))
			if c.refused && err == nil {
				t.Fatal("a second declaration in one file must be REFUSED — before this check it was " +
					"read by nothing, so the estate looked complete in review and was not")
			}
			if !c.refused && err != nil {
				t.Fatalf("must load: %v", err)
			}
			if c.refused {
				// §1.8: the diagnosis has to name the file and say what to do instead.
				for _, want := range []string{"views/x.yaml", "ONE declaration per file", "Split it"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("diagnosis omits %q: %v", want, err)
					}
				}
			}
		})
	}
}

// The check must be in the SHARED path, not in each per-kind parser — fifteen copies of a check is
// how the original defect came to have fifteen copies. Asserted through ParseDir so it fails if a
// future refactor moves the call into one parser and leaves the rest uncovered.
func TestMultiDocumentIsRefusedThroughParseDir(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"views", "workflows"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	two := "name: a\nselector:\n  kinds: [host]\n---\nname: b\nselector:\n  kinds: [host]\n"
	if err := os.WriteFile(filepath.Join(root, "views", "pair.yaml"), []byte(two), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ParseDir(root, nil)
	if err == nil {
		t.Fatal("ParseDir must refuse a multi-document declaration file — this is the path the daemon " +
			"uses at boot, and it is where the silent drop actually happened")
	}
	if !strings.Contains(err.Error(), "pair.yaml") {
		t.Errorf("the error must name the offending file: %v", err)
	}
}
