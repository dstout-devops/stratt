// Package overlay is the G6 defaults/override MERGE engine (ADR-0083 §5, ADR-0055
// guardrail 6): it folds an ORDERED sequence of explicit layers — Blueprint defaults
// (base) then Intent/Assignment values then per-environment overlays — into one
// resolved value map, carrying full provenance.
//
// The charter line this walks (§2.4/§4.1, the anti-GPO axiom): "sane defaults +
// optional overrides" must NOT re-introduce implicit precedence. So:
//   - There is NO priority / weight / order / precedence FIELD anywhere, and there is
//     no rung order among DECLARATIONS. §2.4 admits exactly two resolutions —
//     exclusive-fails-compile and additive-union — and both are implemented here.
//   - A layer is either YIELDING (a Blueprint's defaults: overridable by definition,
//     "unset takes the default", which is not a contest) or DECLARING (an Intent spec,
//     an Assignment's values: an operator asserting a value).
//   - Scalars: exactly ONE declaring layer may set a path. Two declaring layers setting
//     one path is an EXCLUSIVE DOUBLE-CLAIM and fails, naming both layers (ADR-0083 §5,
//     which specified this and whose exclusive half was never implemented — scalars used
//     to silently overwrite, which is the last-writer-wins the charter forbids).
//   - Lists: ADDITIVE UNION (ensure-contains) — a later layer never silently drops an
//     earlier layer's elements; it can only add. This is the §2.4 additive claim, so
//     lists are deliberately NOT subject to the exclusive rule. The consequence is worth
//     knowing: no layer can NARROW a list, so list-valued per-environment variation
//     requires the earlier layer not to set the list at all.
//   - Type conflicts across layers (a list over a scalar, a map over a list) FAIL
//     loudly (§1.8) — never a silent cross-type coercion. Scalar-over-scalar of a
//     different JSON type is also never coerced; a number is not a string.
//
// Every layer that touches a path is still recorded in Provenance, so a resolved value
// is traceable to the layer that produced it (§1.8 descent) even though it can no longer
// be the product of a silent override.
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
// (e.g. "blueprint:web-server/defaults", "intent:tls-app", "assignment:prod-web") and
// appears verbatim in both Provenance and any double-claim error, so an operator reads
// the offending layer rather than an index. Layer carries no weight/priority/order field
// by design (§2.4 anti-GPO).
type Layer struct {
	Name   string
	Values map[string]any
	// Yielding marks a layer whose values are DEFAULTS. A default is overridable by
	// definition — "unset takes the default" is not two operators declaring one fact —
	// so a yielding layer never claims a path and never collides with one. It also never
	// overwrites a value a DECLARING layer already set, which makes the semantic
	// order-independent: a default fills what is unset regardless of its position.
	//
	// Everything else is a declaration, and declarations are co-equal: see the package
	// doc. This flag is what keeps "defaults + overrides" from being a precedence ladder.
	Yielding bool
}

// Provenance maps a dotted field path to the ordered list of layer names that set it.
// The LAST entry is the effective source; earlier entries are values it explicitly
// overrode (scalars) or added onto (lists). Every resolved value is thus traceable to
// its layer — there is no hidden precedence to reconstruct after the fact.
type Provenance map[string][]string

// Merge folds ordered layers (defaults first) into a resolved value map + provenance.
// Deterministic; returns an error on a cross-type conflict or an exclusive double-claim
// between two declaring layers.
func Merge(layers []Layer) (map[string]any, Provenance, error) {
	out := map[string]any{}
	prov := Provenance{}
	// claimed records, per scalar path, the DECLARING layer that set it — the state the
	// exclusive rule needs. Lists are absent by design (additive union, §2.4).
	claimed := map[string]string{}
	for _, l := range layers {
		if err := mergeInto(out, l.Values, l, "", prov, claimed); err != nil {
			return nil, nil, fmt.Errorf("overlay: layer %q: %w", l.Name, err)
		}
	}
	return out, prov, nil
}

// mergeInto folds src (one layer's values) onto dst at the given path prefix.
func mergeInto(dst, src map[string]any, l Layer, prefix string, prov Provenance, claimed map[string]string) error {
	layer := l.Name
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
				if err := mergeInto(dm, sv, l, path, prov, claimed); err != nil {
					return err
				}
				continue
			}
			dm, ok := existing.(map[string]any)
			if !ok {
				return fmt.Errorf("path %q: layer sets a map over a %s from an earlier layer", path, kindOf(existing))
			}
			if err := mergeInto(dm, sv, l, path, prov, claimed); err != nil {
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
			// scalar (string/number/bool/nil): exactly one DECLARING layer may set it.
			if existing, ok := dst[k]; ok {
				switch existing.(type) {
				case map[string]any, []any:
					return fmt.Errorf("path %q: layer sets a scalar over a %s from an earlier layer", path, kindOf(existing))
				}
			}
			if l.Yielding {
				// A default fills only what is unset. It must never overwrite a value a
				// declaring layer already asserted — otherwise "defaults" would beat
				// declarations whenever the layer order put them last, which is the
				// precedence this design exists to remove.
				//
				// A yield is deliberately NOT recorded in Provenance. Provenance means
				// "the layers that produced this value, last one effective"; a default
				// that stood down produced nothing, and tagging it would both break that
				// invariant and emit a layer name matching no Layer.Name. In the normal
				// defaults-first order the history IS visible, because the default writes
				// before any claim exists and the declaration then appends to it.
				if _, declared := claimed[path]; declared {
					continue
				}
				dst[k] = sv
				prov[path] = append(prov[path], layer)
				continue
			}
			// §2.4 exclusive claim (ADR-0083 §5): two co-equal declarations of one value
			// cannot be resolved without inventing precedence, so this fails instead. The
			// message names the OTHER layer — the wrap in Merge names this one — and says
			// what to do, because the fix is not obvious the first time (§1.8).
			if prev, declared := claimed[path]; declared {
				return fmt.Errorf(
					"path %q is already declared by layer %q: two co-equal declarations of one value cannot be "+
						"resolved without implicit precedence (§2.4, the anti-GPO axiom), so exactly one layer may "+
						"set it — to make this an environment-level decision, remove %q from %q",
					path, prev, path, prev)
			}
			claimed[path] = layer
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
