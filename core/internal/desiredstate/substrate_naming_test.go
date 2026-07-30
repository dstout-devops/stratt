package desiredstate

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// TestNoDeclarationNamesASubstrate is ADR-0151 D1's rule, made a guard instead of a habit: no
// Intent, Blueprint, Assignment or View may name a substrate. A declaration that does cannot
// migrate, because the name IS the coupling.
//
// The defect this catches SHIPPED, briefly: an `Intent/Compute` called `kube-app`, invented to make
// a build pass after a mixed-substrate placement was refused. It made the build work by making the
// declaration mean less — an application tier with a substrate in its name and a substrate-shaped
// hole where its topology used to be.
//
// WHAT IT DELIBERATELY DOES NOT CHECK: substrate-SHAPED params. An Intent carrying
// `{instanceType: t3.micro, ami: …}` is not naming a substrate; it is carrying an opaque payload
// the resolved provider's Action Contract types (ADR-0110, ADR-0151 D4). Flagging those would be
// this guard inventing a rule the ADR explicitly declined to make, and it would fire on every
// honest fleet declaration. The name and an explicit `substrate:` key are the two places the
// coupling is unambiguous, so they are the two places this looks.
func TestNoDeclarationNamesASubstrate(t *testing.T) {
	repo := filepath.Join("..", "..", "..")

	var dirs []string
	for _, kind := range []string{"intents", "blueprints", "assignments", "views"} {
		for _, pat := range []string{
			filepath.Join(repo, "estate", kind),
			filepath.Join(repo, "plugins", "*", "estate", kind),
			filepath.Join(repo, "plugins", "*", "demo", "estate", kind),
			filepath.Join(repo, "demos", "*", "estate", kind),
		} {
			m, err := filepath.Glob(pat)
			if err != nil {
				t.Fatal(err)
			}
			dirs = append(dirs, m...)
		}
	}

	checked := 0
	for _, dir := range dirs {
		for _, f := range yamlsIn(t, dir) {
			var decl struct {
				Name      string `yaml:"name"`
				Substrate string `yaml:"substrate"`
			}
			decodeYAML(t, f, &decl)
			checked++
			if decl.Substrate != "" {
				t.Errorf("%s: declares substrate %q — only a PROVIDER declares a substrate, and only "+
					"a capability-binding selects one (ADR-0151 D1/D2). A declaration that names its "+
					"substrate cannot migrate", f, decl.Substrate)
			}
			// The name is matched on WORD boundaries, so `kube-app` and `aws_web` are caught while
			// `vmware-tools` or a legitimate `service-vm-agent` are not simply for containing the
			// letters. Substrate tokens come from the closed set, so a new substrate inherits this
			// rule instead of needing someone to remember it.
			for _, sub := range append(types.Substrates(), "kube", "k8s", "ec2") {
				if hasWord(decl.Name, sub) {
					t.Errorf("%s: declaration named %q contains the substrate token %q — an Intent, "+
						"Blueprint, Assignment or View never names where it runs (ADR-0151 D1). Name it "+
						"for WHAT it is; the substrate is chosen once, in a capability-binding",
						f, decl.Name, sub)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no declarations scanned — the estate layout changed and this guard is checking nothing")
	}
	t.Logf("checked %d declarations", checked)
}

// hasWord reports whether tok appears in name as a whole hyphen/underscore-delimited word.
func hasWord(name, tok string) bool {
	for _, part := range strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	}) {
		if part == tok {
			return true
		}
	}
	return false
}
