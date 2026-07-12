// Package template is the platform's one audited value-binding mechanism
// (ADR-0024): explicit field lookup, never an expression language (charter §1
// permanent non-goal — "no new configuration languages"). A `{{.ns.a.b}}`
// token is a dotted-path lookup into a named namespace map; there are no
// operators, conditionals, loops, or evaluation. It backs the Intent compiler
// (`spec`), Trigger event binding (`event`), and parametrized Views (`param`)
// so the "explicit lookup only" guarantee has a single implementation and a
// single review surface.
package template

import (
	"fmt"
	"regexp"
	"strings"
)

// tokenRe matches a single {{.ns.path.to.field}} reference. The namespace and
// each path segment are bare identifiers — no spaces, operators, or calls.
var tokenRe = regexp.MustCompile(`\{\{\s*\.([a-zA-Z][a-zA-Z0-9_]*(?:\.[a-zA-Z0-9_]+)+)\s*\}\}`)

// exactRe matches a string that is EXACTLY one token (nothing around it),
// which resolves to the referenced value with its native type preserved.
var exactRe = regexp.MustCompile(`^\{\{\s*\.([a-zA-Z][a-zA-Z0-9_]*(?:\.[a-zA-Z0-9_]+)+)\s*\}\}$`)

// Namespaces maps a namespace name (spec|event|param) to its data.
type Namespaces map[string]map[string]any

// Substitute walks a JSON-ish value (map, slice, string, or scalar) and
// resolves every `{{.ns.path}}` token. A string that is exactly one token
// takes the resolved value's native type (a number stays a number); a token
// embedded in surrounding text is rendered with fmt.Sprint. Non-string
// scalars pass through unchanged. An unknown namespace or missing field is an
// error (fail-closed — a binding never silently drops).
func Substitute(v any, ns Namespaces) (any, error) {
	switch t := v.(type) {
	case string:
		return substituteString(t, ns)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			r, err := Substitute(val, ns)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			r, err := Substitute(val, ns)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	default:
		return v, nil
	}
}

// SubstituteParams resolves a params map (the common case) and returns a new
// map. A nil map returns nil.
func SubstituteParams(params map[string]any, ns Namespaces) (map[string]any, error) {
	if params == nil {
		return nil, nil
	}
	out, err := Substitute(params, ns)
	if err != nil {
		return nil, err
	}
	return out.(map[string]any), nil
}

func substituteString(s string, ns Namespaces) (any, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	// Exact single-token string → preserve the resolved value's type.
	if m := exactRe.FindStringSubmatch(s); m != nil {
		return lookup(m[1], ns)
	}
	// Embedded token(s) → render into the surrounding text.
	var lookErr error
	out := tokenRe.ReplaceAllStringFunc(s, func(tok string) string {
		path := tokenRe.FindStringSubmatch(tok)[1]
		val, err := lookup(path, ns)
		if err != nil {
			lookErr = err
			return tok
		}
		return fmt.Sprint(val)
	})
	if lookErr != nil {
		return nil, lookErr
	}
	return out, nil
}

// lookup resolves a dotted path "ns.a.b.c" against the namespaces.
func lookup(path string, ns Namespaces) (any, error) {
	segs := strings.Split(path, ".")
	data, ok := ns[segs[0]]
	if !ok {
		return nil, fmt.Errorf("template references unknown namespace %q", segs[0])
	}
	var cur any = map[string]any(data)
	for _, seg := range segs[1:] {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("template path .%s: %q is not an object", path, seg)
		}
		cur, ok = m[seg]
		if !ok {
			return nil, fmt.Errorf("template references unknown field .%s", path)
		}
	}
	return cur, nil
}

// Has reports whether a JSON-ish value contains any template token.
func Has(v any) bool {
	switch t := v.(type) {
	case string:
		return strings.Contains(t, "{{")
	case map[string]any:
		for _, val := range t {
			if Has(val) {
				return true
			}
		}
	case []any:
		for _, val := range t {
			if Has(val) {
				return true
			}
		}
	}
	return false
}

// References returns the set of namespace names any token in v refers to —
// used at declaration time to scope which bindings a context allows (e.g.
// `event` only on event-kind Triggers).
func References(v any) map[string]bool {
	out := map[string]bool{}
	collectRefs(v, out)
	return out
}

func collectRefs(v any, out map[string]bool) {
	switch t := v.(type) {
	case string:
		for _, m := range tokenRe.FindAllStringSubmatch(t, -1) {
			out[strings.SplitN(m[1], ".", 2)[0]] = true
		}
	case map[string]any:
		for _, val := range t {
			collectRefs(val, out)
		}
	case []any:
		for _, val := range t {
			collectRefs(val, out)
		}
	}
}
