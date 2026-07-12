package opentofu

import "encoding/json"

// redactPlan masks sensitive leaves in a `tofu show -json` plan document:
// resource_changes[].change.{before,after} under their *_sensitive markers,
// planned_values / prior_state module trees under sensitive_values, and
// output values flagged sensitive. The event stream is not a secret channel
// (§2.5). Unparseable documents pass through untouched (never worse than
// before) — but tofu's own format is stable JSON, so that path is cold.
func redactPlan(raw json.RawMessage) json.RawMessage {
	var plan map[string]any
	if err := json.Unmarshal(raw, &plan); err != nil {
		return raw
	}
	if rcs, ok := plan["resource_changes"].([]any); ok {
		for _, rc := range rcs {
			m, ok := rc.(map[string]any)
			if !ok {
				continue
			}
			ch, ok := m["change"].(map[string]any)
			if !ok {
				continue
			}
			for _, side := range []string{"before", "after"} {
				if marker, ok := ch[side+"_sensitive"]; ok {
					ch[side] = redactValue(ch[side], marker)
				}
			}
		}
	}
	if ocs, ok := plan["output_changes"].(map[string]any); ok {
		for _, oc := range ocs {
			m, ok := oc.(map[string]any)
			if !ok {
				continue
			}
			for _, side := range []string{"before", "after"} {
				if marker, ok := m[side+"_sensitive"]; ok {
					m[side] = redactValue(m[side], marker)
				}
			}
		}
	}
	for _, section := range []string{"planned_values", "prior_state"} {
		sec, ok := plan[section].(map[string]any)
		if !ok {
			continue
		}
		if section == "prior_state" {
			sec, ok = sec["values"].(map[string]any)
			if !ok {
				continue
			}
		}
		if outs, ok := sec["outputs"].(map[string]any); ok {
			for _, o := range outs {
				if m, ok := o.(map[string]any); ok {
					if sens, _ := m["sensitive"].(bool); sens {
						m["value"] = "(sensitive)"
					}
				}
			}
		}
		if root, ok := sec["root_module"].(map[string]any); ok {
			redactModule(root)
		}
	}
	out, err := json.Marshal(plan)
	if err != nil {
		return raw
	}
	return out
}

func redactModule(mod map[string]any) {
	if resources, ok := mod["resources"].([]any); ok {
		for _, r := range resources {
			m, ok := r.(map[string]any)
			if !ok {
				continue
			}
			if marker, ok := m["sensitive_values"]; ok {
				m["values"] = redactValue(m["values"], marker)
			}
		}
	}
	if children, ok := mod["child_modules"].([]any); ok {
		for _, c := range children {
			if m, ok := c.(map[string]any); ok {
				redactModule(m)
			}
		}
	}
}

// redactValue masks value leaves wherever the marker tree is `true`.
func redactValue(value, marker any) any {
	switch m := marker.(type) {
	case bool:
		if m {
			return "(sensitive)"
		}
	case map[string]any:
		v, ok := value.(map[string]any)
		if !ok {
			return value
		}
		for k, sub := range m {
			if cur, exists := v[k]; exists {
				v[k] = redactValue(cur, sub)
			}
		}
	case []any:
		v, ok := value.([]any)
		if !ok {
			return value
		}
		for i, sub := range m {
			if i < len(v) {
				v[i] = redactValue(v[i], sub)
			}
		}
	}
	return value
}
