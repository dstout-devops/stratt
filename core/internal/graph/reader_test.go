package graph

import (
	"encoding/json"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func TestSelectorSQL(t *testing.T) {
	tests := []struct {
		name      string
		sel       types.ViewSelector
		wantWhere string
		wantArgs  int
		wantErr   bool
	}{
		{
			name:      "empty selector matches all live entities",
			sel:       types.ViewSelector{},
			wantWhere: "e.deleted_at IS NULL",
			wantArgs:  0,
		},
		{
			name:      "kind clause",
			sel:       types.ViewSelector{Kinds: []string{"vm"}},
			wantWhere: "e.deleted_at IS NULL AND e.kind = ANY($1::text[])",
			wantArgs:  1,
		},
		{
			// The charter's view://label:run=X shorthand (§5.1) desugars to this.
			name:      "label clause",
			sel:       types.ViewSelector{Labels: map[string]string{"run": "X"}},
			wantWhere: "e.deleted_at IS NULL AND e.labels @> $1::jsonb",
			wantArgs:  1,
		},
		{
			name: "facet predicate",
			sel: types.ViewSelector{Facets: []types.FacetPredicate{
				{Namespace: "os.kernel", Path: "family", Equals: json.RawMessage(`"linux"`)},
			}},
			wantWhere: "e.deleted_at IS NULL AND EXISTS (SELECT 1 FROM graph.facet f WHERE f.entity_id = e.id AND f.namespace = $1 AND f.value @> $2::jsonb)",
			wantArgs:  2,
		},
		{
			name:    "facet predicate without namespace is rejected",
			sel:     types.ViewSelector{Facets: []types.FacetPredicate{{Equals: json.RawMessage(`1`)}}},
			wantErr: true,
		},
		{
			name:    "facet predicate with invalid JSON is rejected",
			sel:     types.ViewSelector{Facets: []types.FacetPredicate{{Namespace: "a.b", Equals: json.RawMessage(`{`)}}},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			where, args, err := selectorSQL(tt.sel)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got where=%q", where)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if where != tt.wantWhere {
				t.Errorf("where:\n got %q\nwant %q", where, tt.wantWhere)
			}
			if len(args) != tt.wantArgs {
				t.Errorf("args: got %d, want %d", len(args), tt.wantArgs)
			}
		})
	}
}

func TestContainmentDoc(t *testing.T) {
	tests := []struct {
		path   string
		equals string
		want   string
	}{
		{"", `"linux"`, `"linux"`},
		{"family", `"linux"`, `{"family":"linux"}`},
		{"os.family", `"linux"`, `{"os":{"family":"linux"}}`},
		{"cpus", `4`, `{"cpus":4}`},
	}
	for _, tt := range tests {
		got, err := containmentDoc(tt.path, json.RawMessage(tt.equals))
		if err != nil {
			t.Fatalf("path %q: %v", tt.path, err)
		}
		if string(got) != tt.want {
			t.Errorf("path %q: got %s, want %s", tt.path, got, tt.want)
		}
	}
}
