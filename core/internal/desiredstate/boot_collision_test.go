package desiredstate

import (
	"os"
	"regexp"
	"testing"
)

// A declared Actuator name must not ALSO be boot-registered in Go (§2.4, ADR-0103).
//
// This is a real defect that shipped, not a hypothetical. `plugins/crossplane/estate/actuators/
// crossplane.yaml` declared an Actuator named `crossplane` while main.go boot-registered the same
// name behind STRATT_CROSSPLANE_PLUGIN_ADDR. RegisterActuator refuses a duplicate, boot ran first
// and won — so on any floor with that env var set, the declaration a reader could see in Git was
// the one being REJECTED, and the grant actually in force was the hardcoded one. "Which grant is
// live?" was unanswerable from the estate, and answered wrongly by anyone who looked.
//
// It is the same failure the ansible/script migration recorded and the cert-issuer one repeated:
// two registration paths for one name. The rule only holds if something checks it, so this reads
// main.go's literal registrations rather than restating them in a list that would drift.

var bootActuatorRe = regexp.MustCompile(`(?:registerPluginActuator|plugins\.RegisterActuator)\(\s*"([a-z0-9-]+)"`)

func TestNoDeclaredActuatorIsAlsoBootRegistered(t *testing.T) {
	src, err := os.ReadFile("../../cmd/strattd/main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	boot := map[string]bool{}
	for _, m := range bootActuatorRe.FindAllStringSubmatch(string(src), -1) {
		boot[m[1]] = true
	}
	if len(boot) == 0 {
		// Not a pass. If the boot registrations are all gone this test has no subject, but a
		// regex that silently matches nothing is how a ratchet quietly stops ratcheting.
		t.Skip("no boot-registered Actuators remain — delete this test with the last one")
	}

	decls, err := ParseDir("../../../estate", nil)
	if err != nil {
		t.Fatalf("parse reference estate: %v", err)
	}
	if len(decls.Actuators) == 0 {
		// The symmetric guard. A test that parses an empty declaration set passes for the wrong
		// reason, and a path typo would do exactly that.
		t.Fatal("the reference estate declares no Actuators — this test would pass vacuously")
	}
	for _, a := range decls.Actuators {
		if boot[a.Name] {
			t.Errorf("Actuator %q is BOTH declared and boot-registered in main.go — RegisterActuator "+
				"refuses the second (§2.4), boot wins, and the declaration in Git is the one rejected. "+
				"Migrate the grant into the declaration and delete the boot block (ADR-0103); do not "+
				"rename around it", a.Name)
		}
	}
}
