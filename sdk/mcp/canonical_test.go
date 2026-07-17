package mcp

import (
	"encoding/json"
	"testing"
)

// TestCanonicalHash_LockedVector pins the canonical form + hash for a known schema so
// a silent change to the canonicalization (which would break every existing rung-3
// pin) is caught (ADR-0053 MF-4). Key ordering, no HTML-escaping, no trailing newline.
func TestCanonicalHash_LockedVector(t *testing.T) {
	// keys deliberately out of order + an HTML-sensitive char to exercise both rules.
	in := json.RawMessage(`{"type":"object","properties":{"q":{"type":"string","description":"a<b & c"}},"required":["q"]}`)
	hash, canon, err := CanonicalHash(in)
	if err != nil {
		t.Fatal(err)
	}
	wantCanon := `{"properties":{"q":{"description":"a<b & c","type":"string"}},"required":["q"],"type":"object"}`
	if string(canon) != wantCanon {
		t.Fatalf("canonical form drifted:\n got %s\nwant %s", canon, wantCanon)
	}
	// A second call on a byte-different-but-equal doc yields the SAME hash (canonical).
	h2, _, _ := CanonicalHash(json.RawMessage(`{"required":["q"],"type":"object","properties":{"q":{"type":"string","description":"a<b & c"}}}`))
	if h2 != hash {
		t.Fatalf("equal docs must canonicalize to the same hash: %s vs %s", hash, h2)
	}
}

func TestContractName(t *testing.T) {
	if got := ContractName("srv", "tool"); got != "mcp/srv/tool.input" {
		t.Fatalf("ContractName = %q", got)
	}
}
