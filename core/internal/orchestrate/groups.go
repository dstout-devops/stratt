package orchestrate

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/dstout-devops/stratt/types"
)

// ── group membership: keyed_groups without an expression language (ADR-0161 D2) ──────────────────
//
// The CORE resolves which named partitions a target belongs to; the Actuator renders them in its own
// tool's syntax (ADR-0161 D1). Every DISTINCT value of a declared key becomes a group, so a value
// nobody enumerated still produces one — which is the whole point of keyed_groups and the thing a
// View-per-value cannot do.
//
// WHY THIS IS NOT A NEW CONFIGURATION LANGUAGE (§1 non-goal): a GroupKey addresses a label key or a
// Facet namespace+path. That is the same structured predicate DATA a ViewSelector already carries.
// There are no operators, no conditionals and nothing evaluated — ADR-0024's rule, held by the shape
// rather than by convention.

// groupsFor resolves one Entity's group names from the Step's declared keys.
//
// facets is the batch-read Facet document per namespace for THIS entity, so this function does no
// I/O — it is a pure function of what the caller already read, which is what makes it unit-testable
// without a store and keeps the per-target cost at zero extra queries.
func groupsFor(e types.Entity, facets map[string]json.RawMessage, keys []types.GroupKey) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	var out []string
	for i, k := range keys {
		if (k.Label == "") == (k.Facet == nil) {
			// Both or neither: a key with two sources needs a rule to pick between them, and §2.4
			// refuses to have one. Caught here as well as at declaration because a Workflow can be
			// updated between validation and this Run.
			return nil, fmt.Errorf("groupBy[%d]: set exactly one of label or facet", i)
		}
		var value string
		if k.Label != "" {
			value = e.Labels[k.Label]
		} else {
			value = facetValueAt(facets[k.Facet.Namespace], k.Facet.Path)
		}
		if value == "" {
			// NO BUCKET FOR THE ABSENT. A target without the attribute joins no group from this key.
			// An "unknown"/"ungrouped" group would be the core asserting a value the graph does not
			// carry (§1.2), and a play targeting it would be converging hosts on the strength of a
			// fact nobody observed.
			continue
		}
		name := groupName(k.Prefix, value)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	// Sorted, because the rendered inventory's byte-stability is a §1.8 property other tests pin:
	// two Runs over one target set must produce identical bytes or they cannot be compared during
	// descent, and map iteration order would break that on its own.
	sort.Strings(out)
	return out, nil
}

// facetValueAt reads a dotted path out of a Facet document and renders it as a group-usable scalar.
//
// Only SCALARS group. An object or array addressed by a path yields no value rather than a
// stringified blob: "the group whose name is a JSON object" is not a thing anyone meant, and
// silently producing one would be worse than producing nothing.
func facetValueAt(raw json.RawMessage, path string) string {
	if len(raw) == 0 {
		return ""
	}
	var cur any
	if err := json.Unmarshal(raw, &cur); err != nil {
		return ""
	}
	for _, seg := range strings.Split(path, ".") {
		if seg == "" {
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur, ok = m[seg]
		if !ok {
			return ""
		}
	}
	switch v := cur.(type) {
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	case float64:
		// %v renders 8 as "8" and 8.5 as "8.5" — no trailing ".000000".
		return fmt.Sprintf("%v", v)
	case json.Number:
		return v.String()
	default:
		return ""
	}
}

// groupName builds a tool-safe group name from a prefix and an observed value.
//
// SANITISATION IS LOSSY AND THE COLLISION IS REAL, so it is stated rather than discovered: ansible
// group names admit letters, digits and underscore, and warn on anything else. `eu-west-1` and
// `eu.west.1` are different observed values that both sanitise to `eu_west_1`, so two regions could
// merge into one group.
//
// It is accepted rather than solved, and the reasoning is that every alternative is worse: rejecting
// the value refuses a Run over data the estate does not control (a cloud provider's region names are
// not ours to reject); hashing produces group names no human can read in a play; and passing the raw
// value through hands ansible a name it warns about and treats inconsistently. What is NOT accepted
// is the collision being invisible — `renderTargets` reports the merge, and callers surface it.
func groupName(prefix, value string) string {
	var b strings.Builder
	if prefix != "" {
		b.WriteString(sanitizeGroupToken(prefix))
		b.WriteByte('_')
	}
	b.WriteString(sanitizeGroupToken(value))
	name := strings.Trim(b.String(), "_")
	if name == "" || !isGroupStart(rune(name[0])) {
		// A name must not start with a digit: ansible accepts it and then a play referencing it
		// reads as a number in some contexts. Prefixing is the least surprising repair.
		if name == "" {
			return ""
		}
		name = "g_" + name
	}
	return name
}

func sanitizeGroupToken(s string) string {
	var b strings.Builder
	prevUnderscore := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevUnderscore = false
		default:
			if !prevUnderscore {
				b.WriteByte('_')
				prevUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

func isGroupStart(r rune) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}
