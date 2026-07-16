package cellrouter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// kind classifies how a request federates.
type kind int

const (
	kindNone      kind = iota // pass straight to the local handler
	kindList                  // scatter-gather + merge a list read
	kindPoint                 // route a single-Entity read to its home Cell
	kindAggregate             // scatter-gather + group-SUM a per-identity aggregate
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

// extractAudit keys an audit event by `at` (per-Cell `seq` is not comparable
// across Cells), with a (cell, seq) tiebreak for total order (ADR-0044 slice 4).
func extractAudit(raw json.RawMessage) itemKey {
	var v struct {
		At   time.Time `json:"at"`
		Seq  int64     `json:"seq"`
		Cell string    `json:"cell"`
	}
	_ = json.Unmarshal(raw, &v)
	return itemKey{ts: v.At, id: v.Cell + ":" + strconv.FormatInt(v.Seq, 10)}
}

var (
	runsAdapter     = adapter{extract: func(r json.RawMessage) itemKey { return extractField(r, "startedAt") }, less: descByTS}
	findingsAdapter = adapter{extract: func(r json.RawMessage) itemKey { return extractField(r, "lastObserved") }, less: descByTS}
	entitiesAdapter = adapter{extract: func(r json.RawMessage) itemKey { return extractField(r, "") }, less: ascByID}
	auditAdapter    = adapter{extract: extractAudit, less: descByTS}
)

// aggAdapter federates a per-identity aggregate (/usage): group by a key, then
// SUM/MAX matching groups across Cells — a client-side merge over per-Cell
// GROUP BYs, no cross-Cell query pushdown (§1.4). No truncation (never cut a
// group).
type aggAdapter struct {
	key     func(map[string]any) string
	combine func(dst, src map[string]any)
}

func numOf(v any) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}

// usageAdapter sums (principal, tool) MCP-usage across Cells: calls/errors add,
// lastCall/principalKind take the max (§1.6 accounting per identity).
var usageAdapter = aggAdapter{
	key: func(m map[string]any) string { return fmt.Sprint(m["principal"]) + "\x00" + fmt.Sprint(m["tool"]) },
	combine: func(dst, src map[string]any) {
		dst["calls"] = numOf(dst["calls"]) + numOf(src["calls"])
		dst["errors"] = numOf(dst["errors"]) + numOf(src["errors"])
		if fmt.Sprint(src["lastCall"]) > fmt.Sprint(dst["lastCall"]) { // rfc3339 lexical == chronological
			dst["lastCall"] = src["lastCall"]
		}
		if fmt.Sprint(src["principalKind"]) > fmt.Sprint(dst["principalKind"]) {
			dst["principalKind"] = src["principalKind"]
		}
	},
}

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
	case p == "/audit":
		return auditAdapter, kindList
	case p == "/usage":
		return adapter{}, kindAggregate
	case isViewEntities(p):
		return entitiesAdapter, kindList
	case isEntityByID(p), isRunByID(p), isWorkflowRunByID(p):
		// A datum lives only in its home Cell (single-writer residency), so a
		// point read is local-if-present-else-ask-the-homing-peer (§1.8). Runs
		// and WorkflowRuns join Entities here so a descent from a parent
		// RunAcrossCells into a peer-homed child Run resolves (ADR-0044 slice 5).
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

// pointByID matches "/<prefix>/{id}" — exactly two slashes and a non-empty id, so
// it never swallows a sub-resource like "/runs/{id}/events" or ".../cancel"
// (three slashes).
func pointByID(p, prefix string) bool {
	return strings.HasPrefix(p, prefix) && strings.Count(p, "/") == 2 && len(p) > len(prefix)
}

// /entities/{id}
func isEntityByID(p string) bool { return pointByID(p, "/entities/") }

// /runs/{id} — NOT /runs/{id}/events or /runs/{id}/cancel (three slashes).
func isRunByID(p string) bool { return pointByID(p, "/runs/") }

// /workflow-runs/{id}
func isWorkflowRunByID(p string) bool { return pointByID(p, "/workflow-runs/") }

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

// mergeAggregate group-merges per-Cell aggregate rows: rows sharing a key are
// combined (SUM/MAX), the rest pass through, output sorted by key for
// determinism. No truncation — never cut a group (ADR-0044 slice 4).
func mergeAggregate(bodies [][]byte, agg aggAdapter) ([]byte, error) {
	groups := map[string]map[string]any{}
	for _, b := range bodies {
		if len(b) == 0 {
			continue
		}
		var arr []map[string]any
		if err := json.Unmarshal(b, &arr); err != nil {
			return nil, err
		}
		for _, item := range arr {
			k := agg.key(item)
			if existing, ok := groups[k]; ok {
				agg.combine(existing, item)
			} else {
				groups[k] = item
			}
		}
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]map[string]any, len(keys))
	for i, k := range keys {
		out[i] = groups[k]
	}
	return json.Marshal(out)
}
