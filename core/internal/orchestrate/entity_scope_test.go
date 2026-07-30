package orchestrate

import (
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// narrowToEntity is ADR-0150 D3: a remediation launched from a Finding converges the Entity that
// drifted, not its whole tier. It NARROWS within the declared View and never widens — the View is
// still what authorization was decided against at launch (ADR-0028), which is why the scope applies
// to the Run rather than to `viewName`.
func TestNarrowToEntity(t *testing.T) {
	ents := []types.Entity{{ID: "e-1"}, {ID: "e-2"}, {ID: "e-3"}}

	// No scope ⇒ the whole View, which is every launch that is not a per-Finding remediation.
	got, err := narrowToEntity(ents, RunInput{ViewName: "managed-web"})
	if err != nil || len(got) != 3 {
		t.Fatalf("an unscoped Run must converge its whole View: %v %d", err, len(got))
	}

	got, err = narrowToEntity(ents, RunInput{ViewName: "managed-web", EntityScope: "e-2"})
	if err != nil {
		t.Fatalf("narrowing to a member must succeed: %v", err)
	}
	if len(got) != 1 || got[0].ID != "e-2" {
		t.Fatalf("the Run must converge exactly the scoped Entity, got %+v", got)
	}

	// A scope that matches nothing is REFUSED, never silently empty. A quietly-empty target set
	// would turn "converge this host" into "converge nothing" and report success — and the
	// per-Entity values (D2) were already resolved FOR that host, so running them against nothing
	// is worse than not running.
	_, err = narrowToEntity(ents, RunInput{ViewName: "managed-web", EntityScope: "e-9"})
	if err == nil {
		t.Fatal("an Entity outside the View must be refused, not silently converge zero targets")
	}
	for _, want := range []string{"e-9", "managed-web"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal must name %q: %v", want, err)
		}
	}
}
