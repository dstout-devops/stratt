package compiler

import (
	"context"
	"errors"
	"fmt"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

// Applier is the compiler's write surface (satisfied by *graph.Store).
type Applier interface {
	RegisterFacetOwner(ctx context.Context, o types.FacetOwner) error
	UpsertBaseline(ctx context.Context, b types.Baseline) error
	DeleteBaseline(ctx context.Context, name string) error
	WriteOrphanFinding(ctx context.Context, baseline, target, severity string, detail []byte) error
	PutAssignmentMembership(ctx context.Context, m graph.AssignmentMembership) error
	DeleteAssignmentMembership(ctx context.Context, assignment string) error
}

// Apply writes a compile Plan. Ownership registration runs first: a namespace
// owned by a different owner surfaces as ErrOwnerConflict — recorded as a
// compile error, and that namespace's Baselines are withheld (never a partial
// apply against a contested claim). Orphan Findings are written before their
// Baselines are pruned (never silently, §2.4).
func (p Plan) Apply(ctx context.Context, a Applier) []string {
	errs := append([]string{}, p.Errors...)

	blocked := map[string]bool{} // namespace → ownership denied this pass
	for _, o := range p.Ownership {
		if err := a.RegisterFacetOwner(ctx, o); err != nil {
			if errors.Is(err, graph.ErrOwnerConflict) {
				blocked[o.Namespace] = true
				errs = append(errs, fmt.Sprintf("blueprint %s cannot claim facet %q: %v", o.OwnerRef, o.Namespace, err))
				continue
			}
			errs = append(errs, fmt.Sprintf("register facet owner %s: %v", o.Namespace, err))
		}
	}

	for _, b := range p.Upserts {
		if ownershipBlocked(b, blocked) {
			continue // withheld: its namespace ownership was denied
		}
		if err := a.UpsertBaseline(ctx, b); err != nil {
			errs = append(errs, fmt.Sprintf("upsert compiled baseline %s: %v", b.Name, err))
		}
	}
	for _, o := range p.Orphans {
		if err := a.WriteOrphanFinding(ctx, o.Baseline, o.Target, o.Severity, o.Detail); err != nil {
			errs = append(errs, fmt.Sprintf("orphan finding %s: %v", o.Baseline, err))
		}
	}
	for _, name := range p.Prunes {
		if err := a.DeleteBaseline(ctx, name); err != nil && !errors.Is(err, graph.ErrNotFound) {
			errs = append(errs, fmt.Sprintf("prune compiled baseline %s: %v", name, err))
		}
	}
	for _, m := range p.Memberships {
		if err := a.PutAssignmentMembership(ctx, m); err != nil {
			errs = append(errs, fmt.Sprintf("put membership %s: %v", m.Assignment, err))
		}
	}
	// Withdrawn Assignments' membership snapshots are dropped after their
	// orphan Finding is written.
	for _, o := range p.Orphans {
		asg := trimTarget(o.Target)
		_ = a.DeleteAssignmentMembership(ctx, asg)
	}
	return errs
}

func ownershipBlocked(b types.Baseline, blocked map[string]bool) bool {
	for _, exp := range b.Expected {
		if blocked[exp.Namespace] {
			return true
		}
	}
	return false
}

func trimTarget(target string) string {
	const prefix = "assignment:"
	if len(target) > len(prefix) && target[:len(prefix)] == prefix {
		return target[len(prefix):]
	}
	return target
}
