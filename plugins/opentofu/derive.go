package opentofu

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// tofuOutputType is one output's type expression + sensitivity, from
// `tofu output -json`.
type tofuOutputType struct {
	Type      json.RawMessage
	Sensitive bool
}

// deriveOutputsSchema derives the Step's rung-2 outputs JSON Schema from the tofu
// output type expressions (charter §2.2 rung 2; ADR-0017). The plugin OWNS this
// derivation (it is the content-expert); the core RECOMPUTES + pins the hash of
// the returned document (no plugin-asserted sha256, §1.5). Ported from the in-tree
// contract.DeriveTofuOutputsSchema so the plugin carries no core dependency.
func deriveOutputsSchema(outputs map[string]tofuOutputType) (json.RawMessage, error) {
	props := map[string]any{}
	names := make([]string, 0, len(outputs))
	for name := range outputs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		o := outputs[name]
		sch, err := tofuTypeToSchema(o.Type)
		if err != nil {
			return nil, fmt.Errorf("output %s: %w", name, err)
		}
		if o.Sensitive {
			sch["description"] = "sensitive: value redacted in event streams"
		}
		props[name] = sch
	}
	doc := map[string]any{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"title":       "opentofu Step outputs",
		"description": "Tool-derived from `tofu output -json` type expressions (charter §2.2 rung 2; ADR-0017).",
		"type":        "object",
		"properties":  props,
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// tofuTypeToSchema maps a tofu/cty type expression to a JSON Schema fragment. Type
// expressions are either a string ("string", "number", "bool", "dynamic") or a
// tuple like ["list", T], ["set", T], ["map", T], ["object", {attr: T, …}],
// ["tuple", [T, …]].
func tofuTypeToSchema(t json.RawMessage) (map[string]any, error) {
	var s string
	if err := json.Unmarshal(t, &s); err == nil {
		switch s {
		case "string":
			return map[string]any{"type": "string"}, nil
		case "number":
			return map[string]any{"type": "number"}, nil
		case "bool":
			return map[string]any{"type": "boolean"}, nil
		case "dynamic":
			return map[string]any{}, nil // any
		default:
			return nil, fmt.Errorf("unknown type %q", s)
		}
	}
	var expr []json.RawMessage
	if err := json.Unmarshal(t, &expr); err != nil || len(expr) < 2 {
		return nil, fmt.Errorf("unparseable type expression %s", t)
	}
	var kind string
	if err := json.Unmarshal(expr[0], &kind); err != nil {
		return nil, err
	}
	switch kind {
	case "list", "set":
		items, err := tofuTypeToSchema(expr[1])
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": items}, nil
	case "map":
		vals, err := tofuTypeToSchema(expr[1])
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "object", "additionalProperties": vals}, nil
	case "object":
		var attrs map[string]json.RawMessage
		if err := json.Unmarshal(expr[1], &attrs); err != nil {
			return nil, err
		}
		props := map[string]any{}
		keys := make([]string, 0, len(attrs))
		for k := range attrs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sch, err := tofuTypeToSchema(attrs[k])
			if err != nil {
				return nil, err
			}
			props[k] = sch
		}
		return map[string]any{"type": "object", "properties": props}, nil
	case "tuple":
		var elems []json.RawMessage
		if err := json.Unmarshal(expr[1], &elems); err != nil {
			return nil, err
		}
		items := make([]any, len(elems))
		for i, e := range elems {
			sch, err := tofuTypeToSchema(e)
			if err != nil {
				return nil, err
			}
			items[i] = sch
		}
		return map[string]any{"type": "array", "prefixItems": items}, nil
	default:
		return nil, fmt.Errorf("unknown type constructor %q", kind)
	}
}
