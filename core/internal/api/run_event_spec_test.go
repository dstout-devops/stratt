package api

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// The SSE tail marshals types.RunEvent straight to the wire — the one response
// body on the API that is NOT built from a generated type, because its transport
// is text/event-stream and oapi-codegen prunes the schema as unreferenced. So the
// published contract and the struct that satisfies it are joined by nothing but
// this test. Without it, components/schemas/RunEvent is a comment: a field could
// be added to the struct and never documented, or documented and never sent, and
// every consumer that is not the first-party UI — the CLI, an MCP agent (§1.6) —
// would be reading a spec that lies.
func TestRunEventSpecMatchesTheWireStruct(t *testing.T) {
	spec, err := GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	schema := spec.Components.Schemas["RunEvent"]
	if schema == nil || schema.Value == nil {
		t.Fatal("components/schemas/RunEvent is missing: the SSE payload must stay documented")
	}

	documented := make([]string, 0, len(schema.Value.Properties))
	for name := range schema.Value.Properties {
		documented = append(documented, name)
	}
	slices.Sort(documented)

	var sent []string
	optional := map[string]bool{}
	rt := reflect.TypeOf(types.RunEvent{})
	for i := range rt.NumField() {
		tag := rt.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		parts := strings.Split(tag, ",")
		sent = append(sent, parts[0])
		optional[parts[0]] = slices.Contains(parts[1:], "omitempty")
	}
	slices.Sort(sent)

	if !slices.Equal(documented, sent) {
		t.Fatalf("RunEvent spec drifted from the struct on the wire:\n  documented: %v\n  sent:       %v", documented, sent)
	}

	// A required property must actually always be present — an omitempty field
	// cannot be required, or the spec promises what the encoder may drop.
	for _, req := range schema.Value.Required {
		if optional[req] {
			t.Errorf("RunEvent.%s is required by the spec but omitempty on the struct", req)
		}
	}

	// level is the field this test exists to protect (ADR-0117 g): its enum must
	// stay in step with the types.RunEvent* constants, since the dispatcher maps
	// the port's TaskEvent.Level onto exactly these.
	level := schema.Value.Properties["level"]
	if level == nil || level.Value == nil {
		t.Fatal("RunEvent.level is undocumented")
	}
	var enum []string
	for _, v := range level.Value.Enum {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("level enum has a non-string member %v", v)
		}
		enum = append(enum, s)
	}
	slices.Sort(enum)
	want := []string{types.RunEventDebug, types.RunEventError, types.RunEventInfo, types.RunEventWarn}
	slices.Sort(want)
	if !slices.Equal(enum, want) {
		t.Fatalf("level enum %v does not match the RunEventLevel constants %v", enum, want)
	}
	// The empty level must NOT be in the enum: "the producer said nothing" is
	// distinct from "the producer said info", and most of the stream predates the
	// field (§1.8 — an absent signal is not a benign one).
	if slices.Contains(enum, "") {
		t.Error("the empty level must stay out of the enum; absence is expressed by omitting the field")
	}
}

// TestRunEventToWireCarriesLevelAndOmitsAbsence: the level must reach a client
// when stated and be ABSENT when not — an empty string on the wire would make
// "the producer said nothing" indistinguishable from a stated value, and JSON has
// a perfectly good way to say nothing (§1.8).
func TestRunEventToWireCarriesLevelAndOmitsAbsence(t *testing.T) {
	warned := runEventToWire(types.RunEvent{
		RunID: "r", Seq: 7, Kind: "unparsed-event", Level: types.RunEventWarn,
		Payload: map[string]any{"message": "could not decode"},
	})
	if warned.Level == nil || *warned.Level != RunEventLevel(types.RunEventWarn) {
		t.Fatalf("a stated WARN must reach the wire, got %v", warned.Level)
	}
	body, err := json.Marshal(warned)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"level":"warn"`) {
		t.Fatalf("level missing from the serialized event: %s", body)
	}

	silent := runEventToWire(types.RunEvent{RunID: "r", Seq: 1, Kind: "stream-end"})
	if silent.Level != nil {
		t.Fatalf("an unstated level must stay absent, got %q", *silent.Level)
	}
	body, err = json.Marshal(silent)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "level") {
		t.Fatalf("an unstated level must not appear on the wire at all: %s", body)
	}
}
