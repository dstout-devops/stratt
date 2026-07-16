package desiredstate

import (
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// TestValidateCell proves the Cell CaC declaration rules (ADR-0044): a Cell
// needs a name (never "local"), a region, and an absolute-URL endpoint the
// federation router can dial.
func TestValidateCell(t *testing.T) {
	cases := []struct {
		name string
		c    types.Cell
		ok   bool
	}{
		{"valid", types.Cell{Name: "eu-west", Region: "eu-west-1", Endpoint: "https://eu.stratt.internal"}, true},
		{"reserved local", types.Cell{Name: "local", Region: "r", Endpoint: "https://x"}, false},
		{"no name", types.Cell{Region: "r", Endpoint: "https://x"}, false},
		{"no region", types.Cell{Name: "eu", Endpoint: "https://x"}, false},
		{"bad endpoint", types.Cell{Name: "eu", Region: "r", Endpoint: "not-a-url"}, false},
		{"empty endpoint", types.Cell{Name: "eu", Region: "r", Endpoint: ""}, false},
	}
	for _, c := range cases {
		if err := ValidateCell(c.c); (err == nil) != c.ok {
			t.Fatalf("%s: ok=%v err=%v", c.name, c.ok, err)
		}
	}
}

// TestParseCellFile proves a cells/*.yaml round-trips into a CaC-declared Cell.
func TestParseCellFile(t *testing.T) {
	name, c, err := parseCellFile("cells/eu.yaml", []byte("name: eu-west\nregion: eu-west-1\nendpoint: https://eu.stratt.internal\ndescription: primary EU\n"))
	if err != nil {
		t.Fatal(err)
	}
	if name != "eu-west" || c.Region != "eu-west-1" || c.Endpoint != "https://eu.stratt.internal" || c.DeclaredBy != "cac" {
		t.Fatalf("parsed Cell mismatch: %+v", c)
	}
	// An unknown field is rejected (KnownFields).
	if _, _, err := parseCellFile("cells/bad.yaml", []byte("name: eu\nregion: r\nendpoint: https://x\nbogus: 1\n")); err == nil {
		t.Fatal("unknown yaml field must be rejected")
	}
}

// TestValidateCellSet proves the exactly-one authz-home invariant (ADR-0044
// slice 4): a named fleet needs exactly one authzHome Cell (the sole OpenFGA
// tuple writer); a pure single-Cell estate (no declared Cells) is fine.
func TestValidateCellSet(t *testing.T) {
	cell := func(name string, home bool) types.Cell {
		return types.Cell{Name: name, Region: "r", Endpoint: "https://" + name, AuthzHome: home}
	}
	cases := []struct {
		name  string
		cells []types.Cell
		ok    bool
	}{
		{"no cells (single-cell)", nil, true},
		{"one home", []types.Cell{cell("eu", true)}, true},
		{"two cells one home", []types.Cell{cell("eu", true), cell("us", false)}, true},
		{"zero home in a fleet", []types.Cell{cell("eu", false), cell("us", false)}, false},
		{"two homes", []types.Cell{cell("eu", true), cell("us", true)}, false},
	}
	for _, c := range cases {
		if err := validateCellSet(c.cells); (err == nil) != c.ok {
			t.Fatalf("%s: ok=%v err=%v", c.name, c.ok, err)
		}
	}
}
