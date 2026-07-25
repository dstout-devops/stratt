package desiredstate

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// bootRegisteredActuators is the SHRINKING set of Actuator names strattd still registers
// in Go at boot rather than reading from a declaration (ADR-0103's remaining boot blocks).
// A Step may name one of these without the estate declaring it, because the daemon
// conjures it — which is exactly the property ADR-0103 is retiring.
//
// This map is the migration tracker, and it is deliberately a test fixture rather than a
// comment: shrinking it is how a migration is proven, and forgetting to shrink it fails
// the census assertion below. ADR-0117 follow-up (k) removed `ansible` and `script` from
// it (they are now estate/actuators/{ansible,script}.yaml); `mcp` and the openbao-provided
// `cert-issuer` are what remain.
var bootRegisteredActuators = map[string]string{
	"mcp":         "in-tree MCP Actuator, pending its own extraction slice (ADR-0046)",
	"cert-issuer": "registered by the openbao plugin block on STRATT_OPENBAO_PLUGIN_ADDR (ADR-0050)",
}

// TestEstateDeclaresTheActuatorsItNames is the guard that ADR-0117 follow-up (k) needs and
// that nothing else provides.
//
// Migrating `ansible`/`script` off their boot blocks moved them from "the daemon always has
// them" to "the estate declares them, or the floor does not have them." That is the intended
// CaC posture, but it converts a deleted file into a RUNTIME failure a long way from the
// deletion: eight Steps and Triggers in the reference estate name `ansible`, and without
// estate/actuators/ansible.yaml each of them fails at dispatch with an unknown-actuator error
// on a live floor. Nothing in the build would have said a word.
//
// So: every per-target Actuator a Step/Trigger/Baseline names must either be declared in the
// same estate tree, or be an explicitly-tracked boot-registered name above. Actions
// (`action: vcenter/create-vm`) are deliberately NOT checked — several plugins still wire
// their Action names via boot env (values-demo-vsphere, awsec2), so an Action reference
// legitimately resolves outside the estate today.
func TestEstateDeclaresTheActuatorsItNames(t *testing.T) {
	trees := map[string]string{"reference": estateRoot}
	dirs, err := filepath.Glob(filepath.Join(demosRoot, "*", "estate"))
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range dirs {
		trees["demo/"+filepath.Base(filepath.Dir(dir))] = dir
	}
	if len(trees) < 2 {
		t.Fatalf("found only the reference estate — this guard must not pass by finding nothing")
	}
	for label, dir := range trees {
		t.Run(label, func(t *testing.T) {
			if _, err := os.Stat(dir); err != nil {
				t.Skipf("estate not found at %s (%v)", dir, err)
			}
			decls, err := ParseDir(dir, nil)
			if err != nil {
				t.Fatalf("%s does not parse/validate: %v", dir, err)
			}
			declared := map[string]bool{}
			for _, a := range decls.Actuators {
				declared[a.Name] = true
			}
			check := func(kind, owner, step, actuator string) {
				if actuator == "" || declared[actuator] {
					return
				}
				if why, ok := bootRegisteredActuators[actuator]; ok {
					t.Logf("%s %q %s names boot-registered Actuator %q (%s) — still owed a CaC declaration (ADR-0103)",
						kind, owner, step, actuator, why)
					return
				}
				t.Errorf("%s %q %s names Actuator %q, which %s does not declare and no boot block registers — "+
					"the Step would fail at dispatch with an unknown Actuator",
					kind, owner, step, actuator, dir)
			}
			for _, wf := range decls.Workflows {
				for _, st := range wf.Steps {
					check("workflow", wf.Name, "step "+st.Name, st.Actuator)
				}
			}
			for _, tr := range decls.Triggers {
				check("trigger", tr.Name, "", tr.Actuator)
			}
			for _, bl := range decls.Baselines {
				check("baseline", bl.Name, "", bl.Actuator)
			}
		})
	}
}

// TestReferenceEstateDeclaresTheEEJobActuators pins the two declarations follow-up (k)
// created, and the properties the boot blocks used to guarantee in Go.
//
// The grant is the part worth pinning rather than merely the file's existence. The boot
// registration inlined ansible's bounded MF3 facet ceiling; a declaration that dropped a
// namespace would not fail to parse — it would quietly refuse fact write-back at run time,
// which is the failure mode ADR-0117 D5c was about. And `script`'s dryRunnable: false is a
// safety property: flip it to true and a dry-run Step against an arbitrary script would be
// EXECUTED and reported as a preview.
func TestReferenceEstateDeclaresTheEEJobActuators(t *testing.T) {
	if _, err := os.Stat(estateRoot); err != nil {
		t.Skipf("reference estate not found at %s (%v)", estateRoot, err)
	}
	decls, err := ParseDir(estateRoot, nil)
	if err != nil {
		t.Fatalf("reference estate does not parse/validate: %v", err)
	}
	byName := map[string]types.Actuator{}
	for _, a := range decls.Actuators {
		byName[a.Name] = a
	}

	ansible, ok := byName["ansible"]
	if !ok {
		t.Fatal("reference estate no longer declares the `ansible` Actuator — since it left its boot block " +
			"(ADR-0117 k) this declaration is the ONLY thing that makes it exist on a floor")
	}
	if len(ansible.JobCommand) == 0 || ansible.Address != "" {
		t.Errorf("ansible must stay an EE-Job Actuator (jobCommand, no address) — the §3 GPLv3 boundary; got jobCommand=%v address=%q",
			ansible.JobCommand, ansible.Address)
	}
	if !ansible.DryRunnable {
		t.Error("ansible must stay dryRunnable — --check is what makes a Baseline/dry-run Step against it legal")
	}
	// The MF3 ceiling the boot block inlined, verbatim. Not a subset: a missing namespace is a
	// silently-refused write-back, and an ADDED one is a widened grant that no ADR authorized.
	wantNS := []string{
		"os.kernel",
		"os.hardening.sysctl", "os.hardening.sshd", "os.hardening.filesystem",
		"os.hardening.auditd", "os.hardening.services",
		"fileset.content", "access.grants",
		"app.config",
	}
	got := map[string]bool{}
	for _, ns := range ansible.FacetNamespaces {
		got[ns] = true
	}
	for _, ns := range wantNS {
		if !got[ns] {
			t.Errorf("ansible's declared grant is missing facet namespace %q — a Step's facetWriteScope is "+
				"INTERSECTED with this list, so write-back to it would be refused at run time, not at parse time", ns)
		}
		delete(got, ns)
	}
	for ns := range got {
		t.Errorf("ansible's declared grant WIDENED to facet namespace %q, which the boot registration did not carry — "+
			"a grant grows only by decision (§2.1)", ns)
	}

	// elevatedInputs is what makes ansible's typed `become` Control-gateable (ADR-0122 D3,
	// closing ADR-0117 D1). Pinned rather than left to review because losing it is INVISIBLE: the
	// estate still parses, every Run still works, and the only thing that changes is that a
	// Control gating on `stratt.change/privileged` stops firing — a governance check that silently
	// passes everything, which is the worst failure shape available here (§1.8).
	if !slices.Contains(ansible.ElevatedInputs, "become.enabled") {
		t.Errorf("ansible must declare become.enabled as an elevating input, got %v — without it core "+
			"derives no privileged label and any Control gating on privilege escalation silently "+
			"never fires", ansible.ElevatedInputs)
	}

	script, ok := byName["script"]
	if !ok {
		t.Fatal("reference estate no longer declares the `script` Actuator — the dev-assert e2e and any " +
			"script Step on a full-estate floor depend on it existing")
	}
	if script.DryRunnable {
		t.Error("script must NOT be dryRunnable: an arbitrary script has no read-only capability, so a dry-run " +
			"Step naming it must be rejected at launch rather than executed and reported as a preview (§1.8)")
	}
	if len(script.FacetNamespaces) != 0 || len(script.IdentitySchemes) != 0 {
		t.Errorf("script must carry an EMPTY grant (it proposes no Facets and no identity write-back); got facets=%v schemes=%v",
			script.FacetNamespaces, script.IdentitySchemes)
	}
}
