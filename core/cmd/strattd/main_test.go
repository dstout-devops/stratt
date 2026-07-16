package main

import (
	"testing"

	"github.com/dstout-devops/stratt/core/internal/authz"
	"github.com/dstout-devops/stratt/types"
)

// TestCACOwnsMappedTeam proves the §2.1 one-owner guard: a team is flagged only
// when it is BOTH a SCIM-mapping target AND has its membership declared in CaC.
func TestCACOwnsMappedTeam(t *testing.T) {
	mapped := map[string]bool{"platform": true, "security": true}
	cases := []struct {
		name string
		cac  []authz.Tuple
		want string
	}{
		{
			name: "conflict: CaC declares a mapped team's member",
			cac:  []authz.Tuple{{User: "principal:bob", Relation: authz.RelationMember, Object: "team:platform"}},
			want: "platform",
		},
		{
			name: "no conflict: CaC member on an UNmapped team",
			cac:  []authz.Tuple{{User: "principal:bob", Relation: authz.RelationMember, Object: "team:ops"}},
			want: "",
		},
		{
			name: "no conflict: CaC grants a role to a mapped team (policy, not membership)",
			cac:  []authz.Tuple{{User: "team:platform#member", Relation: authz.RelationRunner, Object: "view:prod"}},
			want: "",
		},
		{
			name: "no conflict: empty CaC",
			cac:  nil,
			want: "",
		},
	}
	for _, c := range cases {
		if got := cacOwnsMappedTeam(c.cac, mapped); got != c.want {
			t.Errorf("%s: cacOwnsMappedTeam = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestLeaderLeaseName proves Cell-scoped lease naming (ADR-0044): the local Cell
// keeps the legacy name (backward-compat) and named Cells are disjoint.
func TestLeaderLeaseName(t *testing.T) {
	if got := leaderLeaseName(""); got != "strattd-leader" {
		t.Fatalf("empty Cell must keep the legacy lease name, got %q", got)
	}
	if got := leaderLeaseName("local"); got != "strattd-leader" {
		t.Fatalf("local Cell must keep the legacy lease name, got %q", got)
	}
	if got := leaderLeaseName("us-east"); got != "strattd-leader-us-east" {
		t.Fatalf("named Cell must be Cell-scoped, got %q", got)
	}
	if leaderLeaseName("us-east") == leaderLeaseName("eu-west") {
		t.Fatal("distinct Cells must produce distinct lease names")
	}
}

// TestIsAuthzHome proves the authz-home predicate (ADR-0044 slice 4): 'local'
// owns authz only when no named Cells are declared; a named fleet's authz-home
// is exactly the flagged Cell; a 'local' daemon in a named fleet loud-fails.
func TestIsAuthzHome(t *testing.T) {
	cell := func(name string, home bool) types.Cell { return types.Cell{Name: name, AuthzHome: home} }
	fleet := []types.Cell{cell("eu", true), cell("us", false)}

	if h, err := isAuthzHome("local", nil); !h || err != nil {
		t.Fatalf("single-cell 'local' must be authz-home: h=%v err=%v", h, err)
	}
	if h, err := isAuthzHome("eu", fleet); !h || err != nil {
		t.Fatalf("the flagged Cell must be authz-home: h=%v err=%v", h, err)
	}
	if h, err := isAuthzHome("us", fleet); h || err != nil {
		t.Fatalf("a non-flagged Cell must not be authz-home: h=%v err=%v", h, err)
	}
	if _, err := isAuthzHome("local", fleet); err == nil {
		t.Fatal("'local' in a named fleet must loud-fail")
	}
}
