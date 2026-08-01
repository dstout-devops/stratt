package graph

import (
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// ADR-0155 D1's rule, tested WITHOUT Postgres on purpose. The graph package's other tests are
// database-gated and SKIP without one — which is precisely how an inert mechanism stays green
// in this repo — and this rule is the subtle part of the decision.
func TestUnambiguousLoginKeys(t *testing.T) {
	user := func(scimID, name string) types.SCIMIdentity {
		return types.SCIMIdentity{SCIMID: scimID, UserName: name}
	}

	t.Run("a single IdP keys every named user", func(t *testing.T) {
		got := unambiguousLoginKeys(map[string][]types.SCIMIdentity{
			"okta": {user("u1", "alice"), user("u2", "bob")},
		})
		if got["u1"] != "alice" || got["u2"] != "bob" || len(got) != 2 {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("lowercased, so a Controller's JSmith joins an IdP's jsmith", func(t *testing.T) {
		got := unambiguousLoginKeys(map[string][]types.SCIMIdentity{"okta": {user("u1", "JSmith")}})
		if got["u1"] != "jsmith" {
			t.Fatalf("got %q — RFC 7643 makes userName caseExact:false, so normalising cannot "+
				"merge two people and it makes the join survive a case difference", got["u1"])
		}
	})

	t.Run("a name claimed by TWO IdPs keys NEITHER", func(t *testing.T) {
		got := unambiguousLoginKeys(map[string][]types.SCIMIdentity{
			"okta":  {user("u1", "jsmith"), user("u2", "alice")},
			"entra": {user("e1", "JSMITH"), user("e2", "carol")},
		})
		for _, ambiguous := range []string{"u1", "e1"} {
			if k, present := got[ambiguous]; present {
				t.Errorf("%s got key %q — two candidate people is not a person, and picking one "+
					"is the implicit precedence §2.4 refuses", ambiguous, k)
			}
		}
		// …and the unambiguous ones in the same IdPs are unaffected.
		if got["u2"] != "alice" || got["e2"] != "carol" {
			t.Errorf("one collision must not suppress the rest: %v", got)
		}
	})

	t.Run("the same name TWICE IN ONE IdP is not ambiguity", func(t *testing.T) {
		// SCIM says userName is unique per provider, so this should not happen — but if the
		// registry ever holds it, both rows are the same directory's claim and the name still
		// resolves to one IdP. Suppressing here would be inventing a conflict.
		got := unambiguousLoginKeys(map[string][]types.SCIMIdentity{
			"okta": {user("u1", "dup"), user("u2", "DUP")},
		})
		if got["u1"] != "dup" || got["u2"] != "dup" {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("a blank username has nothing to key on", func(t *testing.T) {
		got := unambiguousLoginKeys(map[string][]types.SCIMIdentity{
			"okta": {user("u1", ""), user("u2", "   ")},
		})
		if len(got) != 0 {
			t.Fatalf("got %v — an empty key would collide with every other unnamed identity", got)
		}
	})

	t.Run("no IdPs at all", func(t *testing.T) {
		if got := unambiguousLoginKeys(nil); len(got) != 0 {
			t.Fatalf("got %v", got)
		}
	})
}
