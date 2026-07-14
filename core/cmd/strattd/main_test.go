package main

import (
	"testing"

	"github.com/dstout-devops/stratt/core/internal/authz"
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
