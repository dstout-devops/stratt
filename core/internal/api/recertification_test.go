package api

import "testing"

// TestBuildGrantReviews covers the recertification fold (ADR-0036): observed
// grants gain their hosts + a managed flag; a desired grant with no observed
// host still appears (drifted-off, under review); an observed grant with no
// desired Intent is flagged unmanaged (rogue). The order is deterministic.
func TestBuildGrantReviews(t *testing.T) {
	observed := map[accessGrant]map[string]bool{
		{Subject: "alice", Kind: "group", Scope: "wheel"}: {"h1": true, "h2": true},
		{Subject: "mallory", Kind: "sudo", Scope: "ALL"}:  {"h1": true}, // rogue: not desired
	}
	desired := map[accessGrant]struct{ intent, assignment string }{
		{Subject: "alice", Kind: "group", Scope: "wheel"}:           {"alice-wheel", "a1"},
		{Subject: "bob", Kind: "authorized_key", Scope: "SHA256:x"}: {"bob-key", "a2"}, // desired, not observed
	}
	lookup := func(g accessGrant) (string, string, bool) {
		o, ok := desired[g]
		return o.intent, o.assignment, ok
	}
	desiredAll := []accessGrant{
		{Subject: "alice", Kind: "group", Scope: "wheel"},
		{Subject: "bob", Kind: "authorized_key", Scope: "SHA256:x"},
	}

	rv := buildGrantReviews(observed, lookup, desiredAll)
	if len(rv) != 3 {
		t.Fatalf("expected 3 grant reviews (alice, bob, mallory), got %d: %+v", len(rv), rv)
	}
	// Sorted by principal: alice, bob, mallory.
	if rv[0].Subject != "alice" || !rv[0].Managed || len(rv[0].Hosts) != 2 || rv[0].Intent == nil || *rv[0].Intent != "alice-wheel" {
		t.Fatalf("alice review: %+v", rv[0])
	}
	if rv[1].Subject != "bob" || !rv[1].Managed || len(rv[1].Hosts) != 0 {
		t.Fatalf("bob (desired, unobserved) review: %+v", rv[1])
	}
	if rv[2].Subject != "mallory" || rv[2].Managed || rv[2].Intent != nil {
		t.Fatalf("mallory (rogue) review must be unmanaged: %+v", rv[2])
	}

	// Hash is stable across recomputation and changes when hosts change.
	h1 := hashGrantReviews(rv)
	if h1 != hashGrantReviews(buildGrantReviews(observed, lookup, desiredAll)) {
		t.Fatal("grant-set hash must be stable for the same set")
	}
	observed[accessGrant{Subject: "alice", Kind: "group", Scope: "wheel"}]["h3"] = true
	if h1 == hashGrantReviews(buildGrantReviews(observed, lookup, desiredAll)) {
		t.Fatal("grant-set hash must change when a grant gains a host (re-attestation needed)")
	}
}
