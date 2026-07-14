package graph

import (
	"context"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// TestSCIMRegistry exercises the SCIM identity registry end to end against a
// real Postgres: provision → group → membership → projected tuples → deactivate
// drops the grant → tombstone. Skips when no dev DB is reachable.
func TestSCIMRegistry(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	// Register an IdP mapping the group "Platform Eng" → team:platform.
	if err := store.UpsertIDP(ctx, types.SCIMIdP{
		Name: "okta", TokenHash: "aa",
		GroupMappings: []types.GroupMapping{{Group: "Platform Eng", Team: "platform"}},
	}); err != nil {
		t.Fatal(err)
	}

	// Provision two users; alice is in the mapped group, bob is not.
	alice := types.SCIMIdentity{IDP: "okta", SCIMID: "u-alice", UserName: "alice@corp", PrincipalID: "sub-alice", Active: true}
	bob := types.SCIMIdentity{IDP: "okta", SCIMID: "u-bob", UserName: "bob@corp", PrincipalID: "sub-bob", Active: true}
	for _, i := range []types.SCIMIdentity{alice, bob} {
		if err := store.UpsertIdentity(ctx, i); err != nil {
			t.Fatal(err)
		}
	}

	// Provision the group and add alice.
	if err := store.UpsertGroup(ctx, types.SCIMGroup{IDP: "okta", SCIMID: "g-eng", DisplayName: "Platform Eng"}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceGroupMembers(ctx, "okta", "g-eng", []string{"u-alice"}); err != nil {
		t.Fatal(err)
	}

	// alice is a member of the mapped team; bob is not.
	memberships := mustMemberships(t, store, ctx)
	if !memberships["sub-alice|platform"] {
		t.Errorf("alice should be projected into team:platform; got %v", memberships)
	}
	if memberships["sub-bob|platform"] {
		t.Error("bob is not in the group; must not be projected")
	}

	// LookupActive gates the request-time deactivation block.
	assertActive(t, store, ctx, "sub-alice", true, true)
	assertActive(t, store, ctx, "nobody", false, false) // unknown-to-SCIM is not gated

	// Deactivate alice → her projected membership drops AND she reads inactive.
	if err := store.SetIdentityActive(ctx, "okta", "u-alice", false); err != nil {
		t.Fatal(err)
	}
	memberships = mustMemberships(t, store, ctx)
	if memberships["sub-alice|platform"] {
		t.Error("deactivated alice must not project a membership")
	}
	assertActive(t, store, ctx, "sub-alice", true, false)

	// Reactivate, then tombstone (SCIM DELETE) → gone from projection + lookup.
	if err := store.SetIdentityActive(ctx, "okta", "u-alice", true); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteIdentity(ctx, "okta", "u-alice"); err != nil {
		t.Fatal(err)
	}
	memberships = mustMemberships(t, store, ctx)
	if memberships["sub-alice|platform"] {
		t.Error("tombstoned alice must not project a membership")
	}
	assertActive(t, store, ctx, "sub-alice", false, false) // tombstoned = unknown

	// MappedTeams feeds the one-owner guard.
	teams, err := store.MappedTeams(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !teams["platform"] {
		t.Errorf("platform should be a mapped team; got %v", teams)
	}

	// Cleanup keeps the shared dev DB tidy (testStore uses a throwaway DB, but
	// deleting the config is cheap and documents the sole-writer paths).
	_ = store.DeleteGroup(ctx, "okta", "g-eng")
	_ = store.DeleteIDP(ctx, "okta")
}

func mustMemberships(t *testing.T, store *Store, ctx context.Context) map[string]bool {
	t.Helper()
	ms, err := store.ProjectedMemberships(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, m := range ms {
		out[m.PrincipalID+"|"+m.Team] = true
	}
	return out
}

func assertActive(t *testing.T, store *Store, ctx context.Context, principal string, wantFound, wantActive bool) {
	t.Helper()
	found, active, err := store.LookupActive(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	if found != wantFound || active != wantActive {
		t.Errorf("LookupActive(%s) = (found=%v,active=%v), want (found=%v,active=%v)", principal, found, active, wantFound, wantActive)
	}
}
