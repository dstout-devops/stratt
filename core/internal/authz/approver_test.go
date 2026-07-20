package authz

import (
	"context"
	"testing"
)

type fakeAuthz struct{ member map[string]bool }

func (f fakeAuthz) Check(_ context.Context, principal, relation, object string) (bool, error) {
	return f.member[principal+"|"+relation+"|"+object], nil
}
func (f fakeAuthz) CheckHealth(_ context.Context) error { return nil }

func TestApproverAuthorized(t *testing.T) {
	az := fakeAuthz{member: map[string]bool{
		"bob|" + RelationMember + "|team:platform": true,
	}}
	cases := []struct {
		name       string
		principal  string
		principals []string
		teams      []string
		want       bool
	}{
		{"explicit principal matches", "alice", []string{"alice"}, nil, true},
		{"team member matches via Check", "bob", nil, []string{"platform"}, true},
		{"non-member of team denied", "carol", nil, []string{"platform"}, false},
		{"not in principals list denied", "dave", []string{"alice"}, nil, false},
		{"no approvers at all denied", "alice", nil, nil, false},
	}
	for _, c := range cases {
		got, err := ApproverAuthorized(context.Background(), az, c.principal, c.principals, c.teams)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Fatalf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
