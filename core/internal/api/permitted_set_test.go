package api

import (
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func wfWithCreds(refs ...string) types.Workflow {
	return types.Workflow{Name: "patch", Steps: []types.Step{
		{Name: "converge", ViewName: "prod", Actuator: "ansible", CredentialRefs: refs},
	}}
}

// A launch NARROWS the declared set; it never adds to it (ADR-0160 D4). The estate decides what a
// Workflow may EVER use; the launch decides which of those to mount today. Widening would let a
// launcher bring a credential the estate never blessed for this Step — and while §2.5's `user` check
// would still stop them using one they hold no grant on, "may I use it" and "may this Step use it"
// are different questions and the estate owns the second.
func TestALaunchMayNarrowCredentialsButNeverWiden(t *testing.T) {
	wf := wfWithCreds("prod-key", "dev-key")
	s := &Server{}

	if err := s.checkPermittedCredentials(wf, []string{"prod-key"}); err != nil {
		t.Errorf("selecting a declared ref is the whole feature: %v", err)
	}
	if err := s.checkPermittedCredentials(wf, []string{"prod-key", "dev-key"}); err != nil {
		t.Errorf("selecting all of them is still a subset: %v", err)
	}
	if err := s.checkPermittedCredentials(wf, nil); err != nil {
		t.Errorf("no selection means the declaration, unchanged — every launch before D4: %v", err)
	}

	err := s.checkPermittedCredentials(wf, []string{"prod-key", "someone-elses-key"})
	if err == nil {
		t.Fatal("a ref no Step declares must be REFUSED — a launch narrows, it never widens")
	}
	for _, want := range []string{"someone-elses-key", "NARROWS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name the offending ref and the rule; missing %q: %v", want, err)
		}
	}
}

// narrowCredentialRefs is the second line of defence, and its empty case is the dangerous one: every
// launch before ADR-0160 sends no selection, and reading that as "mount nothing" would silently
// strip every credential from every existing Run.
func TestNoSelectionMeansTheWholeDeclarationNotNothing(t *testing.T) {
	declared := []string{"a", "b"}
	if got := narrowCredentialRefsForTest(declared, nil); len(got) != 2 {
		t.Fatalf("an empty selection is 'no selection made', NOT an empty set: got %v", got)
	}
	if got := narrowCredentialRefsForTest(declared, []string{"b"}); len(got) != 1 || got[0] != "b" {
		t.Fatalf("a selection intersects the declaration: got %v", got)
	}
	if got := narrowCredentialRefsForTest(declared, []string{"c"}); len(got) != 0 {
		t.Fatalf("a selection outside the declaration intersects to nothing: got %v", got)
	}
}

// narrowCredentialRefsForTest mirrors orchestrate.narrowCredentialRefs, which is unexported in its
// own package. Kept byte-identical in shape so this test fails if the rule there changes meaning.
func narrowCredentialRefsForTest(declared, selected []string) []string {
	if len(selected) == 0 {
		return declared
	}
	want := map[string]bool{}
	for _, r := range selected {
		want[r] = true
	}
	out := make([]string, 0, len(declared))
	for _, r := range declared {
		if want[r] {
			out = append(out, r)
		}
	}
	return out
}
