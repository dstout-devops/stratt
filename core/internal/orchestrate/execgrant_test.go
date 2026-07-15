package orchestrate

import (
	"context"
	"strings"
	"testing"
)

type fakeAuthz struct {
	allow bool
	err   error
}

func (f fakeAuthz) Check(_ context.Context, _, _, _ string) (bool, error) { return f.allow, f.err }
func (f fakeAuthz) CheckHealth(_ context.Context) error                   { return nil }

// TestCheckExecutionGrant covers the ADR-0028 View-scoped execution gate: an
// empty Principal and an ungranted Principal are both denied (deny-by-default),
// a granted one passes, and the denials are terminal (ExecutionDenied) so the
// Run fails rather than retrying.
func TestCheckExecutionGrant(t *testing.T) {
	ctx := context.Background()

	// Empty principal → denied without even consulting authz.
	empty := &Activities{Authz: fakeAuthz{allow: true}}
	if err := empty.CheckExecutionGrant(ctx, RunInput{ViewName: "prod"}); err == nil ||
		!strings.Contains(err.Error(), "authenticated principal") {
		t.Fatalf("empty principal must be denied, got %v", err)
	}

	// Ungranted principal → ExecutionDenied naming the view.
	denied := &Activities{Authz: fakeAuthz{allow: false}}
	err := denied.CheckExecutionGrant(ctx, RunInput{Principal: "p", ViewName: "prod"})
	if err == nil || !strings.Contains(err.Error(), "lacks runner on view:prod") {
		t.Fatalf("ungranted principal must be denied, got %v", err)
	}

	// Granted → nil.
	granted := &Activities{Authz: fakeAuthz{allow: true}}
	if err := granted.CheckExecutionGrant(ctx, RunInput{Principal: "p", ViewName: "prod"}); err != nil {
		t.Fatalf("granted principal must pass, got %v", err)
	}
}
