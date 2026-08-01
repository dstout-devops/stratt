package controller

import (
	"encoding/json"
	"strings"
	"testing"
)

// ── ADR-0155 D2 · the correlation edge, and what it does NOT claim ───────────────────────

// The edge targets a POINTABLE key on an entity the SCIM projector owns. This plugin writes
// no identity fact — `identity.subject` and `identity.name` are solely the projector's
// (§2.1, and ADR-0130 D1 is explicit that claiming either is a registration error).
func TestUserCarriesTheCorrelationEdgeAndOwnsNoIdentity(t *testing.T) {
	c := &Client{ctrlID: "ctrl-a"}
	ents, err := c.Normalize(&Snapshot{Users: []User{
		{ID: 60, Username: "JSmith", Email: "j@example.com", IsActive: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	rels := ents[0].GetRelations()
	if len(rels) != 1 || rels[0].GetType() != "same-account-as" {
		t.Fatalf("the correlation edge is missing: %v", rels)
	}
	// LOWERCASED on both sides. RFC 7643 §4.1.1 makes SCIM userName caseExact:false, so two
	// identities cannot differ only by case — normalising cannot merge two people, and it
	// makes the join survive a Controller storing `JSmith` for an IdP's `jsmith`.
	if rels[0].GetToScheme() != "identity.userName" || rels[0].GetToValue() != "jsmith" {
		t.Errorf("edge target = %s:%s", rels[0].GetToScheme(), rels[0].GetToValue())
	}
	// The entity still owns only ansible.user.
	if len(ents[0].GetFacets()) != 1 {
		t.Fatalf("facets = %v", ents[0].GetFacets())
	}
	if _, claimed := ents[0].GetFacets()["identity.subject"]; claimed {
		t.Error("claiming identity.subject is a registration error, not a preference (§2.1)")
	}
	if _, claimed := ents[0].GetLabels()["identity.name"]; claimed {
		t.Error("identity.name has one owner and it is the SCIM projector")
	}
}

// An account with no username has nothing to correlate ON. No edge beats an edge pointing at
// the empty string, which would resolve to whatever else happened to have no name.
func TestNamelessAccountDrawsNoEdge(t *testing.T) {
	c := &Client{ctrlID: "ctrl-a"}
	ents, err := c.Normalize(&Snapshot{Users: []User{{ID: 61, Username: "   "}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ents[0].GetRelations()) != 0 {
		t.Fatalf("a blank username must draw no edge: %v", ents[0].GetRelations())
	}
}

// ── AWX-012 · custom credential types ────────────────────────────────────────────────────

// `managed: false` is the value the whole namespace exists for: a credential of a CUSTOM type
// has no equivalent on the other side of a cutover until that type's fields and injectors
// exist there.
func TestCredentialTypeProjectsWhatMustBeReproduced(t *testing.T) {
	c := &Client{ctrlID: "ctrl-a"}
	var ct CredentialType
	if err := json.Unmarshal([]byte(`{
		"id":42,"name":"ACME Vault","kind":"cloud","managed":false,
		"inputs":{"fields":[
			{"id":"url","type":"string"},
			{"id":"token","type":"string","secret":true}],
		 "required":["url","token"]},
		"injectors":{"env":{"ACME_TOKEN":"{{ token }}"},"extra_vars":{"acme_url":"{{ url }}"}}}`), &ct); err != nil {
		t.Fatal(err)
	}
	ents, err := c.Normalize(&Snapshot{CredentialTypes: []CredentialType{ct}})
	if err != nil {
		t.Fatal(err)
	}
	var facet map[string]any
	if err := json.Unmarshal(ents[0].GetFacets()[KindCredentialType], &facet); err != nil {
		t.Fatal(err)
	}
	if facet["managed"] != false || facet["kind"] != "cloud" {
		t.Errorf("facet = %v", facet)
	}
	fields := facet["fields"].([]any)
	if len(fields) != 2 || fields[0] != "token" || fields[1] != "url" {
		t.Errorf("field names, sorted: %v", fields)
	}
	// WHICH fields are secret is the actionable half: those are the ones that must arrive as a
	// brokered CredentialRef on the other side (§2.5).
	secret := facet["secretFields"].([]any)
	if len(secret) != 1 || secret[0] != "token" {
		t.Errorf("secretFields = %v", secret)
	}
	modes := facet["injectorModes"].([]any)
	if len(modes) != 2 || modes[0] != "env" || modes[1] != "extra_vars" {
		t.Errorf("injector modes, sorted: %v", modes)
	}
}

// The injector TEMPLATES are not projected. They are not a §2.5 risk — a template names a
// field rather than carrying one — but they are arbitrary operator text that the delivery
// mode already summarises for every question a migration asks.
func TestInjectorTemplatesAreNotProjected(t *testing.T) {
	c := &Client{ctrlID: "ctrl-a"}
	var ct CredentialType
	if err := json.Unmarshal([]byte(`{"id":42,"name":"t","managed":false,
		"injectors":{"env":{"ACME_TOKEN":"{{ token }}-SENTINEL"}}}`), &ct); err != nil {
		t.Fatal(err)
	}
	ents, err := c.Normalize(&Snapshot{CredentialTypes: []CredentialType{ct}})
	if err != nil {
		t.Fatal(err)
	}
	if blob := searchable(t, ents); strings.Contains(blob, "SENTINEL") {
		t.Errorf("template text reached the graph: %s", blob)
	}
}

// A managed (built-in) type with no injectors block projects cleanly — an empty list, not a
// missing key, so a reader never has to distinguish absent from empty.
func TestManagedTypeWithNoInjectorsProjectsEmptyLists(t *testing.T) {
	c := &Client{ctrlID: "ctrl-a"}
	ents, err := c.Normalize(&Snapshot{CredentialTypes: []CredentialType{
		{ID: 1, Name: "Machine", Kind: "ssh", Managed: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var facet map[string]any
	if err := json.Unmarshal(ents[0].GetFacets()[KindCredentialType], &facet); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"fields", "secretFields", "injectorModes"} {
		if facet[key] == nil {
			t.Errorf("%s is nil — an empty list beats a missing key", key)
		}
	}
	if facet["managed"] != true {
		t.Error("a built-in type must say it is one")
	}
}
