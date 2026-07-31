package desiredstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// probeEstate is the smallest tree ParseDir accepts: a views/ directory must exist.
func probeEstate(t *testing.T, blueprint string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "views"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "blueprints/bp.yaml", blueprint)
	return dir
}

const qualifierProbeBlueprint = `name: bp
version: 1
for: Intent/Application
severity: warning
routes:
  - observe:
      namespace: app.config
      qualifier: "{{.spec.package}}"
      path: port
      equals: "8080"
    claim: exclusive
`

// A Blueprint route declaring `observe.qualifier` is REFUSED at load, in this release.
//
// ADR-0152 lands in two migrations and this is the first. The claim key and the Facet primary key
// must widen TOGETHER (D4), and right now only the claim key has: graph.facet is still keyed
// (entity_id, namespace, prov_source_id), so two Blueprints declaring different qualifiers on one
// Entity would COMPILE — the conflict detector correctly sees two distinct claims — and then both
// write through ON CONFLICT on that three-column key. The second Run would match the first's row
// and DO UPDATE it, flipping its qualifier. Not an error. A silent last-writer-wins, which is
// exactly the failure the qualifier was introduced to abolish.
//
// So the field is refused rather than half-honoured. A declaration that reads as permitted and is
// honoured by something OTHER than what it says is worse than one that is refused (§1.8) — and the
// refusal has to EXPLAIN, or an author following the ADR hits `field qualifier not found in type`
// from KnownFields and concludes the ADR is fiction.
func TestADeclaredQualifierIsRefusedUntilTheContractRelease(t *testing.T) {
	dir := probeEstate(t, qualifierProbeBlueprint)

	_, err := ParseDir(dir, nil)
	if err == nil {
		t.Fatal("a route declaring observe.qualifier must be refused while graph.facet's primary key " +
			"is still three columns — otherwise two qualified claims compile and then silently " +
			"overwrite each other's row (ADR-0152 D4)")
	}
	msg := err.Error()
	for _, want := range []string{"qualifier", "ADR-0152", "primary key", "overwrite"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal must explain the two-release sequencing (missing %q): %s", want, msg)
		}
	}
	// It must also say what to DO, not only what is wrong.
	if !strings.Contains(msg, "Remove the qualifier") {
		t.Errorf("the refusal must name the action that unblocks the author: %s", msg)
	}
}

// The ordinary route — no qualifier — is unaffected.
//
// Asserted because the refusal above is a load-time gate on a field every Blueprint in the estate
// omits: if it ever fired on the empty string, the whole estate would stop parsing.
func TestAnUnqualifiedRouteStillLoads(t *testing.T) {
	dir := probeEstate(t, strings.Replace(qualifierProbeBlueprint,
		"      qualifier: \"{{.spec.package}}\"\n", "", 1))

	if _, err := ParseDir(dir, nil); err != nil {
		t.Fatalf("an unqualified route must load exactly as before: %v", err)
	}
}
