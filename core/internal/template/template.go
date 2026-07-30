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

// NamespaceEntity is the per-Entity namespace (ADR-0150 D2): `{{.entity.<facet>.<path>}}`,
// `{{.entity.name}}`, `{{.entity.id}}`, `{{.entity.identity.<scheme>}}`.
//
// It is DEFERRED by the Intent compiler and resolved when a Finding's remediation launches,
// because a Baseline covers a whole View and only the Finding names an Entity. Declared here, as
// one constant, so the deferring side and the resolving side cannot disagree about its spelling —
// a mismatch would turn every per-Entity binding into an unresolved token that reaches a plugin
// as literal text.
const NamespaceEntity = "entity"

// DeferEntity is the deferral set for the entity namespace, shared by every compile-time
// substitution that a launch-time pass completes.
var DeferEntity = map[string]bool{NamespaceEntity: true}

// Substitute walks a JSON-ish value (map, slice, string, or scalar) and
// resolves every `{{.ns.path}}` token. A string that is exactly one token
// takes the resolved value's native type (a number stays a number); a token
// embedded in surrounding text is rendered with fmt.Sprint. Non-string
// scalars pass through unchanged. An unknown namespace or missing field is an
// error (fail-closed — a binding never silently drops).
func Substitute(v any, ns Namespaces) (any, error) { return subst(v, ns, nil) }

// SubstituteDeferring is Substitute with a set of namespaces left UNRESOLVED: a token naming one of
// them passes through with its `{{.ns.path}}` text intact instead of erroring as an unknown
// namespace, so a later pass can resolve it against values that did not exist yet (ADR-0150 D2).
//
// This is a TWO-STAGE binding, not a relaxation of the fail-closed rule (ADR-0024 D1). Deferral is
// explicit and by name: a namespace nobody deferred is still an error, so a typo'd `{{.entty.x}}`
// fails at compile exactly as it does today. The deferred stage inherits the same rule — whoever
// resolves it later must supply the namespace or fail — and the compiler is what guarantees a
// second stage exists at all.
//
// The precedent is ADR-0024 D3: a parametrized View's selector carries `{{.param.x}}` through
// storage and binds at launch, because launch is the first moment the value is knowable. This is
// the same shape for a value that is per-ENTITY: the Baseline covers a whole View, the Finding
// names one Entity, and only the Finding has an Entity to resolve against.
func SubstituteDeferring(v any, ns Namespaces, deferred map[string]bool) (any, error) {
	return subst(v, ns, deferred)
}

func subst(v any, ns Namespaces, deferred map[string]bool) (any, error) {
	switch t := v.(type) {
	case string:
		return substituteString(t, ns, deferred)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			r, err := subst(val, ns, deferred)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			r, err := subst(val, ns, deferred)
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
	return SubstituteParamsDeferring(params, ns, nil)
}

// SubstituteParamsDeferring is SubstituteParams with deferred namespaces (see SubstituteDeferring).
func SubstituteParamsDeferring(params map[string]any, ns Namespaces, deferred map[string]bool) (map[string]any, error) {
	if params == nil {
		return nil, nil
	}
	out, err := subst(params, ns, deferred)
	if err != nil {
		return nil, err
	}
	return out.(map[string]any), nil
}

func substituteString(s string, ns Namespaces, deferred map[string]bool) (any, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	// Exact single-token string → preserve the resolved value's type. A DEFERRED namespace returns
	// the token text unchanged, so the next stage sees exactly what the author wrote — including
	// its type-preserving exact-token form.
	if m := exactRe.FindStringSubmatch(s); m != nil {
		if isDeferred(m[1], deferred) {
			return s, nil
		}
		return lookup(m[1], ns)
	}
	// Embedded token(s) → render into the surrounding text.
	var lookErr error
	out := tokenRe.ReplaceAllStringFunc(s, func(tok string) string {
		path := tokenRe.FindStringSubmatch(tok)[1]
		if isDeferred(path, deferred) {
			return tok
		}
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
	// MALFORMED-TOKEN GUARD (charter-guardian, 2026-07-30). Anything still carrying `{{` after both
	// matchers had their turn is text that LOOKS like a binding and is not one — a filter
	// expression, a missing brace, a bare `{{.entity}}` with no field. It used to pass through as a
	// literal string, and ADR-0150 D2 is what made that dangerous: a resolved `commonName` of
	// "{{.entity.dns.fqdn | lower}}" is a perfectly good string, satisfies the Contract, and gets
	// ISSUED AS A CERTIFICATE SUBJECT. That is the wrong-subject outcome D2 says no convenience is
	// worth, reached by a typo instead of by a fallback.
	//
	// Deferred tokens are exempt BY CONSTRUCTION — they are removed before the check, because
	// leaving them intact is the entire point of deferral (ADR-0150 D2's two-stage binding).
	if residual := stripDeferred(out, deferred); strings.Contains(residual, "{{") {
		return nil, fmt.Errorf("template %q contains text that looks like a binding but is not a resolvable token — "+
			"the grammar is a dotted field reference only ({{.ns.a.b}}), with no operators, filters or function calls "+
			"(ADR-0024 D1). It is refused rather than passed through as a literal, because a literal that looks like a "+
			"template is indistinguishable from a value someone meant", s)
	}
	return out, nil
}

// stripDeferred removes tokens of deferred namespaces so the malformed-token guard does not fire on
// a binding that is intentionally waiting for its second stage.
func stripDeferred(s string, deferred map[string]bool) string {
	if len(deferred) == 0 {
		return s
	}
	return tokenRe.ReplaceAllStringFunc(s, func(tok string) string {
		if isDeferred(tokenRe.FindStringSubmatch(tok)[1], deferred) {
			return ""
		}
		return tok
	})
}

// isDeferred reports whether a dotted path's NAMESPACE (its first segment) is deferred.
func isDeferred(path string, deferred map[string]bool) bool {
	if len(deferred) == 0 {
		return false
	}
	ns, _, _ := strings.Cut(path, ".")
	return deferred[ns]
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

// LaunchFields returns the FIRST path segment of every {{.launch.<field>...}} token in s — the
// declared inputs a string consumes. Used at declaration to catch an input a Workflow declares and
// no Step binds (ADR-0123 D3): accepted and silently dropped, which `additionalProperties: false`
// makes structurally indistinguishable from consumed.
//
// First segment only, so `{{.launch.params.region}}` reports `params`: the Workflow does consume
// `params`, and core has no business asserting which fields inside an opaque object are used (§1.5).
func LaunchFields(s string) []string {
	var out []string
	for _, m := range tokenRe.FindAllStringSubmatch(s, -1) {
		segs := strings.Split(m[1], ".")
		if len(segs) >= 2 && segs[0] == "launch" {
			out = append(out, segs[1])
		}
	}
	return out
}

// LaunchParamFields returns the keys a string binds from INSIDE the launch's opaque `params`
// — the second segment of `{{.launch.params.<key>}}`. LaunchFields above stops one level up
// and reports only "params", which is enough to know an input is bound and not enough to know
// WHICH provider-shaped values a builder actually requires of an Intent.
//
// That distinction is the difference between a declaration error caught at load and one caught
// by an operator at an approval gate: `params` is opaque to core (§1.5), so nothing validates
// its contents, and Substitute fails CLOSED on a missing field — at launch, after the approval.
func LaunchParamFields(s string) []string {
	var out []string
	for _, m := range tokenRe.FindAllStringSubmatch(s, -1) {
		segs := strings.Split(m[1], ".")
		if len(segs) >= 3 && segs[0] == "launch" && segs[1] == "params" {
			out = append(out, segs[2])
		}
	}
	return out
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

// Paths returns the set of FULL dotted references any token in v makes —
// "launch.commonName", "steps.build.outputs.id" — where References answers only the
// coarser namespace question.
//
// It exists for field-wise declaration checks (ADR-0118 D2): knowing that a Step binds the
// `launch` namespace is not enough to catch `{{.launch.comonName}}`, which is a typo that
// would otherwise survive until the binding failed at dispatch. Namespace-level checking
// cannot see it; path-level checking can.
func Paths(v any) map[string]bool {
	out := map[string]bool{}
	collectPaths(v, out)
	return out
}

func collectPaths(v any, out map[string]bool) {
	switch t := v.(type) {
	case string:
		for _, m := range tokenRe.FindAllStringSubmatch(t, -1) {
			out[m[1]] = true
		}
	case map[string]any:
		for _, val := range t {
			collectPaths(val, out)
		}
	case []any:
		for _, val := range t {
			collectPaths(val, out)
		}
	}
}
