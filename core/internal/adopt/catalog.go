package adopt

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/dstout-devops/stratt/core/internal/graph"
)

// graphCatalog resolves objects from the live projection graph (the always-on catalog).
type graphCatalog struct{ store *graph.Store }

// NewGraphCatalog binds the projection graph as the adopt catalog.
func NewGraphCatalog(store *graph.Store) Catalog { return graphCatalog{store: store} }

// Resolve confirms the kind+identity is projected and derives the native object id from the
// controller-qualified identity ("<ctrlID>/<nativeID>" — the awx Connector's qualifier). The
// source returned is that qualifier prefix (the Controller id), for the adopted-from lineage.
func (g graphCatalog) Resolve(ctx context.Context, kind, identity string) (int, string, bool, error) {
	_, found, err := g.store.EntityIDByIdentity(ctx, kind, identity)
	if err != nil || !found {
		return 0, "", found, err
	}
	slash := strings.LastIndexByte(identity, '/')
	if slash < 0 {
		return 0, "", true, fmt.Errorf("identity %q is not controller-qualified (<ctrlID>/<id>)", identity)
	}
	nativeID, err := strconv.Atoi(identity[slash+1:])
	if err != nil {
		return 0, "", true, fmt.Errorf("identity %q native id %q is not numeric: %w", identity, identity[slash+1:], err)
	}
	return nativeID, identity[:slash], true, nil
}

// LiveExecutions finds the enabled AWX schedules that still `schedules` (launch) the
// template being adopted — the double-execution surface (ADR-0086 §4). It reads the
// incoming schedule→template edges the awx Connector projects, then each schedule's
// ansible.schedule facet, returning the names of those still enabled. Read-only (§1.2).
func (g graphCatalog) LiveExecutions(ctx context.Context, kind, identity string) ([]string, error) {
	if kind != KindTemplate {
		return nil, nil // only templates carry schedules today
	}
	tid, found, err := g.store.EntityIDByIdentity(ctx, kind, identity)
	if err != nil || !found {
		return nil, err
	}
	schedIDs, err := g.store.RelationSources(ctx, tid, "schedules")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, sid := range schedIDs {
		facets, err := g.store.GetFacets(ctx, sid)
		if err != nil {
			return nil, err
		}
		for _, f := range facets {
			if f.Namespace != "ansible.schedule" {
				continue
			}
			var sc struct {
				Name    string `json:"name"`
				Enabled bool   `json:"enabled"`
			}
			if json.Unmarshal(f.Value, &sc) == nil && sc.Enabled {
				out = append(out, sc.Name)
			}
		}
	}
	return out, nil
}
