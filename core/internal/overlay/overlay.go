// Package overlay is the G6 defaults/override MERGE engine (ADR-0083 §5, ADR-0055
// guardrail 6): it folds an ORDERED sequence of explicit layers — Blueprint defaults
// (base) then Intent/Assignment values then per-environment overlays — into one
// resolved value map, carrying full provenance.
//
// The charter line this walks (§2.4/§4.1, the anti-GPO axiom): "sane defaults +
// optional overrides" must NOT re-introduce implicit precedence. So:
//   - There is NO priority / weight / order / precedence FIELD anywhere. The only
//     precedence is the explicit, structural LAYER ORDER (base first) — the same
//     visible layering Kustomize uses, which the operator authors and can read.
//   - Scalars: the last explicit layer that sets a path wins — but every layer that
//     touched the path is recorded in Provenance, so an override is never silent and
//     the value is always traceable to its source layer (§1.8 descent).
//   - Lists: ADDITIVE UNION (ensure-contains) — a later layer never silently drops an
//     earlier layer's elements; it can only add. This is the §2.4 additive claim.
//   - Type conflicts across layers (a list over a scalar, a map over a list) FAIL
//     loudly (§1.8) — never a silent cross-type coercion.
//
// Pure and deterministic: no I/O, no clock, sorted key order. Values are the JSON-ish
// shapes a parsed spec yields (map[string]any / []any / scalars).
package overlay

import (
	"fmt"
	"reflect"
	"sort"
)

// Layer is one explicit, named layer in an override sequence. Name is provenance
// (e.g. "blueprint:web-server/defaults", "assignment:prod-web", "overlay:prod"). The
// POSITION of the layer in the slice is the sole, explicit precedence — Layer carries
// no weight/priority field by design (§2.4 anti-GPO).
type Layer struct {
	Name   string
	Values map[string]any
}

// Provenance maps a dotted field path to the ordered list of layer names that set it.
// The LAST entry is the effective source; earlier entries are values it explicitly
// overrode (scalars) or added onto (lists). Every resolved value is thus traceable to
// its layer — there is no hidden precedence to reconstruct after the fact.
type Provenance map[string][]string

// Merge folds ordered layers (base first) into a resolved value map + provenance.
// Deterministic; returns an error on a cross-type conflict between layers.
func Merge(layers []Layer) (map[string]any, Provenance, error) {
	out := map[string]any{}
	prov := Provenance{}
	for _, l := range layers {
		if err := mergeInto(out, l.Values, l.Name, "", prov); err != nil {
			return nil, nil, fmt.Errorf("overlay: layer %q: %w", l.Name, err)
		}
	}
	return out, prov, nil
}

// mergeInto folds src (one layer's values) onto dst at the given path prefix.
func mergeInto(dst, src map[string]any, layer, prefix string, prov Provenance) error {
	keys := make([]string, 0, len(src))
	for k := range src {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic

	for _, k := range keys {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		switch sv := src[k].(type) {
		case map[string]any:
			existing, ok := dst[k]
			if !ok {
				dm := map[string]any{}
				dst[k] = dm
				if err := mergeInto(dm, sv, layer, path, prov); err != nil {
					return err
				}
				continue
			}
			dm, ok := existing.(map[string]any)
			if !ok {
				return fmt.Errorf("path %q: layer sets a map over a %s from an earlier layer", path, kindOf(existing))
			}
			if err := mergeInto(dm, sv, layer, path, prov); err != nil {
				return err
			}
		case []any:
			existing, ok := dst[k]
			if ok {
				ex, ok := existing.([]any)
				if !ok {
					return fmt.Errorf("path %q: layer sets a list over a %s from an earlier layer", path, kindOf(existing))
				}
				dst[k] = unionAppend(ex, sv)
			} else {
				dst[k] = unionAppend(nil, sv)
			}
			prov[path] = append(prov[path], layer)
		default:
			// scalar (string/number/bool/nil): last explicit layer wins.
			if existing, ok := dst[k]; ok {
				switch existing.(type) {
				case map[string]any, []any:
					return fmt.Errorf("path %q: layer sets a scalar over a %s from an earlier layer", path, kindOf(existing))
				}
			}
			dst[k] = sv
			prov[path] = append(prov[path], layer)
		}
	}
	return nil
}

// unionAppend returns ex followed by every element of add not already present
// (deep-equal), preserving order — the §2.4 additive/ensure-contains semantics.
func unionAppend(ex, add []any) []any {
	out := make([]any, len(ex))
	copy(out, ex)
	for _, e := range add {
		if !containsDeep(out, e) {
			out = append(out, e)
		}
	}
	return out
}

func containsDeep(xs []any, e any) bool {
	for _, x := range xs {
		if reflect.DeepEqual(x, e) {
			return true
		}
	}
	return false
}

func kindOf(v any) string {
	switch v.(type) {
	case map[string]any:
		return "map"
	case []any:
		return "list"
	default:
		return "scalar"
	}
}
