package graph

import (
	"context"
	"testing"
)

// TestGetObservedBy proves the observedBy read surface (ADR-0042): a co-managed
// Entity reports both observing Sources with kind/name/last-seen, and a Source
// that drops the Entity disappears from observedBy. (Helpers live in
// entity_presence_test.go.)
func TestGetObservedBy(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := s.NormalizerProjector()

	chef := syncerProv("chef/syncer", mustSource(t, s, "chef", "acme-chef"))
	puppet := syncerProv("puppet/syncer", mustSource(t, s, "puppet", "acme-puppet"))
	ids, err := p.UpsertEntities(ctx, chef, []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"chef.node.name": "h1", "dns.fqdn": "f1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	eid := ids[0]
	if _, err := p.UpsertEntities(ctx, puppet, []EntityUpsert{
		{Kind: "host", IdentityKeys: map[string]string{"puppet.certname": "h1", "dns.fqdn": "f1"}},
	}); err != nil {
		t.Fatal(err)
	}

	obs, err := s.GetObservedBy(ctx, eid)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 2 {
		t.Fatalf("co-managed Entity must be observedBy 2 Sources, got %d", len(obs))
	}
	// ORDER BY src.name → acme-chef before acme-puppet.
	if obs[0].Name != "acme-chef" || obs[0].Kind != "chef" || obs[1].Name != "acme-puppet" {
		t.Fatalf("observedBy must carry Source kind+name, got %+v", obs)
	}
	if obs[0].LastSeen.IsZero() || obs[1].FirstSeen.IsZero() {
		t.Fatalf("observedBy must carry first/last-seen times, got %+v", obs)
	}

	// Chef drops it → only Puppet remains in observedBy.
	if _, err := p.TombstoneAbsent(ctx, chef, "chef.node.name", []string{}); err != nil {
		t.Fatal(err)
	}
	obs, err = s.GetObservedBy(ctx, eid)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 1 || obs[0].Name != "acme-puppet" {
		t.Fatalf("after Chef drop, observedBy must be exactly Puppet, got %+v", obs)
	}
}
