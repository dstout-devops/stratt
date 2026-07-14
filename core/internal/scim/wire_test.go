package scim

import (
	"encoding/json"
	"testing"
)

func TestParseFilter(t *testing.T) {
	cases := []struct {
		in          string
		field, want string
	}{
		{`userName eq "alice@corp.com"`, "userName", "alice@corp.com"},
		{`externalId eq "abc-123"`, "externalId", "abc-123"},
		{`displayName eq "Platform Eng"`, "displayName", "Platform Eng"},
		{`userName EQ "up"`, "userName", "up"}, // eq is case-insensitive
		{`userName co "x"`, "", ""},            // only eq supported
		{``, "", ""},                           // empty
		{`garbage`, "", ""},                    // malformed
	}
	for _, c := range cases {
		field, value := parseFilter(c.in)
		if field != c.field || value != c.want {
			t.Errorf("parseFilter(%q) = (%q,%q), want (%q,%q)", c.in, field, value, c.field, c.want)
		}
	}
}

func TestActiveChange(t *testing.T) {
	cases := []struct {
		name          string
		body          string
		active, chngd bool
	}{
		{"path+bool false", `{"Operations":[{"op":"replace","path":"active","value":false}]}`, false, true},
		{"path+bool true", `{"Operations":[{"op":"replace","path":"active","value":true}]}`, true, true},
		{"no-path object", `{"Operations":[{"op":"replace","value":{"active":false}}]}`, false, true},
		{"entra string False", `{"Operations":[{"op":"Replace","path":"active","value":"False"}]}`, false, true},
		{"unrelated op", `{"Operations":[{"op":"replace","path":"displayName","value":"x"}]}`, false, false},
		{"empty", `{"Operations":[]}`, false, false},
	}
	for _, c := range cases {
		var p patchWire
		if err := unmarshal(c.body, &p); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		active, changed := p.activeChange()
		if active != c.active || changed != c.chngd {
			t.Errorf("%s: activeChange = (%v,%v), want (%v,%v)", c.name, active, changed, c.active, c.chngd)
		}
	}
}

func TestMemberValues(t *testing.T) {
	arr := patchOp{Value: []byte(`[{"value":"u1"},{"value":"u2"}]`)}
	if got := arr.memberValues(); len(got) != 2 || got[0] != "u1" || got[1] != "u2" {
		t.Errorf("array memberValues = %v", got)
	}
	single := patchOp{Value: []byte(`{"value":"u3"}`)}
	if got := single.memberValues(); len(got) != 1 || got[0] != "u3" {
		t.Errorf("single memberValues = %v", got)
	}
	if got := (patchOp{}).memberValues(); got != nil {
		t.Errorf("empty memberValues = %v, want nil", got)
	}
}

func TestPrincipalID(t *testing.T) {
	if got := principalID(userWire{ExternalID: "ext-1", UserName: "u@c"}); got != "ext-1" {
		t.Errorf("externalId should win: %q", got)
	}
	if got := principalID(userWire{UserName: "u@c"}); got != "u@c" {
		t.Errorf("userName fallback: %q", got)
	}
}

func TestActiveOrDefault(t *testing.T) {
	if !(userWire{}).activeOrDefault() {
		t.Error("absent active must default true")
	}
	f := false
	if (userWire{Active: &f}).activeOrDefault() {
		t.Error("active:false must be false")
	}
}

// unmarshal is a tiny helper so the table tests read cleanly.
func unmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }
