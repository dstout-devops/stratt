package api

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/compiler"
)

// GET /compile writes compiler.Snapshot straight to the wire (server.go,
// GetCompileStatus) rather than building a generated type — the same shape as the SSE
// tail in run_event_spec_test.go, and the same exposure. `task generate:check` proves
// openapi.yaml and server.gen.go agree WITH EACH OTHER; nothing compares either to the
// struct a hand-written handler actually marshals. So a field can be added to the
// compiler's delta and served to every client while the published schema never mentions
// it: the UI's generated schema.d.ts has no property for it, the CLI cannot render it,
// and an MCP agent introspecting the spec is told it does not exist (§1.6 — one
// capability, every surface, means the SPEC is the surface).
//
// This is not hypothetical. ADR-0119 D5's expectationChanges was served-but-undocumented
// exactly this way for the length of one commit, and only a manual read of the generated
// model caught it.
func TestCompileStatusSpecMatchesTheWireStructs(t *testing.T) {
	spec, err := GetSwagger()
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		schema string
		v      any
	}{
		{"CompileStatus", compiler.Snapshot{}},
		{"AssignmentDelta", compiler.AssignmentDelta{}},
		{"ExpectationChange", compiler.ExpectationChange{}},
	} {
		s := spec.Components.Schemas[tc.schema]
		if s == nil || s.Value == nil {
			t.Errorf("components/schemas/%s is missing: the /compile payload must stay documented", tc.schema)
			continue
		}

		documented := make([]string, 0, len(s.Value.Properties))
		for name := range s.Value.Properties {
			documented = append(documented, name)
		}
		slices.Sort(documented)

		var sent []string
		optional := map[string]bool{}
		rt := reflect.TypeOf(tc.v)
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
			t.Errorf("%s spec drifted from the struct on the wire:\n  documented: %v\n  sent:       %v",
				tc.schema, documented, sent)
		}

		// A required property must actually always be present — an omitempty field
		// cannot be required, or the spec promises what the encoder may drop.
		for _, req := range s.Value.Required {
			if optional[req] {
				t.Errorf("%s.%s is required by the spec but omitempty on the struct", tc.schema, req)
			}
		}
	}
}
