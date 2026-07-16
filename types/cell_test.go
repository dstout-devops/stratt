package types

import "testing"

// TestCellScopeToken pins the ONE derivation both strattd and stratt-agent use
// to agree on their NATS scope (ADR-0044 slice 6): "" for the built-in LocalCell
// (byte-identical), the override when set, else the Cell name.
func TestCellScopeToken(t *testing.T) {
	cases := []struct {
		cell, override, want string
	}{
		{"", "", ""},               // unset ⇒ local ⇒ no scope
		{LocalCell, "", ""},        // explicit local ⇒ no scope
		{"local", "", ""},          // literal
		{"eu-west", "", "eu-west"}, // named ⇒ scope is the cell name
		{"eu-west", "euw", "euw"},  // override wins
		{LocalCell, "euw", "euw"},  // an override even overrides local (operator intent)
		{"", "custom", "custom"},   // override with no cell
	}
	for _, c := range cases {
		if got := CellScopeToken(c.cell, c.override); got != c.want {
			t.Errorf("CellScopeToken(%q,%q)=%q want %q", c.cell, c.override, got, c.want)
		}
	}
}

// TestValidCellScopeToken proves the charset gate that keeps a stray '.' or
// NATS wildcard out of a subject token (charter-guardian slice-6 flag #1): a '.'
// would silently inject an extra subject token and reshape the topology.
func TestValidCellScopeToken(t *testing.T) {
	valid := []string{"", "eu", "eu-west", "us1", "a", "cell-01"}
	for _, tok := range valid {
		if !ValidCellScopeToken(tok) {
			t.Errorf("%q should be valid", tok)
		}
	}
	invalid := []string{"eu.west", "eu west", "EU", "eu*", "eu>", "-eu", "eu_west", "a.b.c"}
	for _, tok := range invalid {
		if ValidCellScopeToken(tok) {
			t.Errorf("%q should be REJECTED (would corrupt the NATS subject topology)", tok)
		}
	}
}

// TestScopedStream proves the single-Cell case is byte-identical and a named
// Cell gets a suffixed, upper-cased (legal JetStream) name.
func TestScopedStream(t *testing.T) {
	if got := ScopedStream("STRATT_DISPATCH", ""); got != "STRATT_DISPATCH" {
		t.Errorf("local stream must be byte-identical, got %q", got)
	}
	if got := ScopedStream("STRATT_DISPATCH", "eu-west"); got != "STRATT_DISPATCH_EU-WEST" {
		t.Errorf("named stream scope: got %q", got)
	}
}

// TestScopedSubjectRoot proves the token lands as the SECOND subject token and
// local is byte-identical.
func TestScopedSubjectRoot(t *testing.T) {
	if got := ScopedSubjectRoot("stratt.run.", ""); got != "stratt.run." {
		t.Errorf("local subject root must be byte-identical, got %q", got)
	}
	if got := ScopedSubjectRoot("stratt.run.", "eu"); got != "stratt.eu.run." {
		t.Errorf("named subject root: got %q", got)
	}
	if got := ScopedSubjectRoot("stratt.dispatch", "eu"); got != "stratt.eu.dispatch" {
		t.Errorf("bare root (no trailing dot): got %q", got)
	}
}
