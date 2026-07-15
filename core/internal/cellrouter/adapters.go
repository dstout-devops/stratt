package cellrouter

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"
)

// kind classifies how a request federates.
type kind int

const (
	kindNone  kind = iota // pass straight to the local handler
	kindList              // scatter-gather + merge a list read
	kindPoint             // route a single-Entity read to its home Cell
)

// adapter is the only per-endpoint code: how to extract the total-order sort key
// from one JSON element of a list body. The key mirrors each endpoint's SQL
// ORDER BY (with the id tiebreak added in slice 3) so a cross-Cell merge is
// deterministic. No cross-Cell join/pushdown — merge only (§1.4).
type adapter struct {
	extract func(json.RawMessage) itemKey
	less    func(a, b itemKey) bool
}

type itemKey struct {
	ts time.Time
	id string
}

// descByTS orders newest-first with an ascending id tiebreak — runs
// (started_at DESC, id) and findings (last_observed DESC, id).
func descByTS(a, b itemKey) bool {
	if a.ts.Equal(b.ts) {
		return a.id < b.id
	}
	return a.ts.After(b.ts)
}

// ascByID orders by id ascending — entities (ORDER BY e.id).
func ascByID(a, b itemKey) bool { return a.id < b.id }

func extractField(raw json.RawMessage, tsField string) itemKey {
	var v struct {
		ID           string    `json:"id"`
		StartedAt    time.Time `json:"startedAt"`
		LastObserved time.Time `json:"lastObserved"`
	}
	_ = json.Unmarshal(raw, &v)
	k := itemKey{id: v.ID}
	switch tsField {
	case "startedAt":
		k.ts = v.StartedAt
	case "lastObserved":
		k.ts = v.LastObserved
	}
	return k
}

var (
	runsAdapter     = adapter{extract: func(r json.RawMessage) itemKey { return extractField(r, "startedAt") }, less: descByTS}
	findingsAdapter = adapter{extract: func(r json.RawMessage) itemKey { return extractField(r, "lastObserved") }, less: descByTS}
	entitiesAdapter = adapter{extract: func(r json.RawMessage) itemKey { return extractField(r, "") }, less: ascByID}
)

// classify maps a request to its federation kind (the explicit federated-route
// table — the §1.4 guardrail; anything not listed passes through untouched).
func classify(r *http.Request) (adapter, kind) {
	if r.Method != http.MethodGet {
		return adapter{}, kindNone
	}
	switch p := r.URL.Path; {
	case p == "/runs", p == "/workflow-runs":
		return runsAdapter, kindList
	case p == "/findings":
		return findingsAdapter, kindList
	case isViewEntities(p):
		return entitiesAdapter, kindList
	case isEntityByID(p):
		return adapter{}, kindPoint
	default:
		return adapter{}, kindNone
	}
}

// /views/{name}/entities
func isViewEntities(p string) bool {
	return strings.HasPrefix(p, "/views/") && strings.HasSuffix(p, "/entities") &&
		strings.Count(p, "/") == 3
}

// /entities/{id}
func isEntityByID(p string) bool {
	return strings.HasPrefix(p, "/entities/") && strings.Count(p, "/") == 2 && len(p) > len("/entities/")
}

// mergeList concatenates the per-Cell (already-sorted) JSON arrays, re-sorts in
// total order, truncates to limit, and re-encodes the bare array (no envelope —
// the wire shape is unchanged). Operates on raw JSON so cellrouter never imports
// the api types.
func mergeList(bodies [][]byte, ad adapter, limit int) ([]byte, error) {
	type item struct {
		raw json.RawMessage
		key itemKey
	}
	var items []item
	for _, b := range bodies {
		if len(b) == 0 {
			continue
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(b, &arr); err != nil {
			return nil, err
		}
		for _, raw := range arr {
			items = append(items, item{raw, ad.extract(raw)})
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return ad.less(items[i].key, items[j].key) })
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	out := make([]json.RawMessage, len(items))
	for i, it := range items {
		out[i] = it.raw
	}
	return json.Marshal(out)
}
