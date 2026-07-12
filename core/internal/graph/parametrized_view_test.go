package graph

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/dstout-devops/stratt/types"
)

func TestResolveSelectorWithParams(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := s.NormalizerProjector()
	prov := types.Provenance{WriterKind: types.WriterSyncer, WriterRef: "test/syncer", At: time.Now().UTC()}
	ids, err := p.UpsertEntities(ctx, prov, []EntityUpsert{
		{Kind: "vm", IdentityKeys: map[string]string{"vcenter.uuid": "a"}, Labels: map[string]string{"vcenter.name": "web-01"}},
		{Kind: "vm", IdentityKeys: map[string]string{"vcenter.uuid": "b"}, Labels: map[string]string{"vcenter.name": "web-02"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = ids

	// A parametrized selector binds {{.param.host}} at resolve time to exactly
	// one Entity.
	sel := types.ViewSelector{Kinds: []string{"vm"}, Labels: map[string]string{"vcenter.name": "{{.param.host}}"}}
	ents, err := s.ResolveSelector(ctx, sel, map[string]any{"host": "web-01"}, 0)
	if err != nil {
		t.Fatalf("resolve with params: %v", err)
	}
	if len(ents) != 1 || ents[0].Labels["vcenter.name"] != "web-01" {
		t.Fatalf("parametrized selector must target the named host, got %+v", ents)
	}

	// A missing param for a placeholder fails closed.
	if _, err := s.ResolveSelector(ctx, sel, map[string]any{}, 0); err == nil {
		t.Fatal("missing param must error")
	}

	// A facet-equals placeholder binds type-preserved.
	_ = json.RawMessage(nil)
	self := types.ViewSelector{Kinds: []string{"vm"}, Facets: []types.FacetPredicate{
		{Namespace: "os.kernel", Path: "arch", Equals: json.RawMessage(`"{{.param.arch}}"`)},
	}}
	// os.kernel is unowned here; just confirm binding produces a resolvable
	// query (zero matches, no error).
	if _, err := s.ResolveSelector(ctx, self, map[string]any{"arch": "x86_64"}, 0); err != nil {
		t.Fatalf("facet param bind must resolve: %v", err)
	}
}
