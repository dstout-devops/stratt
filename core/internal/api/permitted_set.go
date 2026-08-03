package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/dstout-devops/stratt/types"
)

// ── the declared permitted set (ADR-0160 D4) ────────────────────────────────────────────────────
//
// AAP lets a launcher pick a credential or an execution environment, bounded by RBAC. Stratt can do
// the same task today only through a Git round-trip — declare another Step, or another Actuator —
// which moves ownership from "a launcher holding `use` on this credential" to "an estate author with
// commit rights". ADR-0160 D1 permits an ownership move; it does not oblige the largest available
// one.
//
// So the estate declares a SET and the launcher chooses within it. That is exactly AAP's shape
// (an admin enables prompting, RBAC bounds the choice) and it keeps both of this repo's gates:
//
//   - the ESTATE still bounds what is possible — a value outside the declared set is refused here,
//     at the door, before a Run exists;
//   - the §2.5 `user` check still runs per credential in ResolveCredentials, unchanged, so a
//     launcher cannot mount a ref they hold no grant on even when the estate permits it.
//
// Neither gate is relaxed. What changes is only WHO picks among things already blessed.

// checkPermittedImage refuses a launch-selected image that the Actuator did not declare.
//
// KEEPS ADR-0117 D3a. D3a says the image IS the content boundary and a Step selects content by
// selecting an Actuator — not that the boundary is a single value. A declared set is still the
// estate deciding what content is permissible. An Actuator that declares no set offers no choice at
// all, which is every Actuator that exists today.
func (s *Server) checkPermittedImage(ctx context.Context, wf types.Workflow, image string) error {
	if image == "" {
		return nil
	}
	for _, st := range wf.Steps {
		if !st.IsActuation() || st.Actuator == "" {
			continue
		}
		act, err := s.Store.GetActuator(ctx, st.Actuator)
		if err != nil {
			return fmt.Errorf("resolve actuator %q: %w", st.Actuator, err)
		}
		if image == act.Image {
			continue // the declaration's own image is trivially permitted
		}
		if !contains(act.Images, image) {
			return fmt.Errorf("image %q is not in the permitted set actuator %q declares (%s) — the "+
				"image is the content boundary (ADR-0117 D3a) and the estate decides what content is "+
				"permissible; a launch may choose among reviewed images, never introduce one. Add it "+
				"to the Actuator's `images:` in Git if it should be selectable",
				image, st.Actuator, describeSet(append([]string{act.Image}, act.Images...)))
		}
	}
	return nil
}

// checkPermittedCredentials refuses a launch-selected credentialRef the Step never declared.
//
// NARROWS, NEVER WIDENS. The Step's `credentialRefs` is the permitted set, so this can only reduce
// what is mounted. Widening would let a launcher bring a credential the estate never blessed for
// this Step — and while the §2.5 `user` check would still stop them using one they hold no grant on,
// "may I use it" and "may this Step use it" are different questions and the estate owns the second.
func (s *Server) checkPermittedCredentials(wf types.Workflow, refs []string) error {
	if len(refs) == 0 {
		return nil
	}
	declared := map[string]bool{}
	for _, st := range wf.Steps {
		for _, r := range st.CredentialRefs {
			declared[r] = true
		}
	}
	for _, r := range refs {
		if !declared[r] {
			return fmt.Errorf("credentialRef %q is not declared by any Step of workflow %q — a launch "+
				"NARROWS the declared set, it never adds to it (ADR-0160 D4). The estate decides what "+
				"this Workflow may ever use; the launch decides which of those to mount", r, wf.Name)
		}
	}
	return nil
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

func describeSet(vs []string) string {
	seen, out := map[string]bool{}, []string{}
	for _, v := range vs {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) == 0 {
		return "none — it declares no selectable images"
	}
	return strings.Join(out, ", ")
}
