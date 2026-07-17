package opentofu

import (
	"encoding/json"
	"fmt"
	"strings"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// tofuMsg is the subset of tofu's machine-readable -json stream this plugin lifts
// into typed TaskEvent fields + drift; everything else passes through as a plain
// info line (the §1.8 descent channel is never dropped).
type tofuMsg struct {
	Type       string `json:"type"`
	Message    string `json:"@message"`
	Level      string `json:"@level"`
	Diagnostic *struct {
		Severity string `json:"severity"`
		Summary  string `json:"summary"`
		Detail   string `json:"detail"`
	} `json:"diagnostic"`
	Changes *struct {
		Add       int    `json:"add"`
		Change    int    `json:"change"`
		Remove    int    `json:"remove"`
		Operation string `json:"operation"`
	} `json:"changes"`
	Change *struct {
		Resource struct {
			Addr string `json:"addr"`
		} `json:"resource"`
		Action string `json:"action"`
	} `json:"change"`
}

// lineWire is one tofu -json line mapped onto the port: a typed TaskEvent for
// descent, an optional redacted drift fragment (ADR-0019), and whether the line
// escalates the workspace to "changed" (statuses only escalate, so a later rc=0
// terminal cannot hide a plan that would change — §1.8).
type lineWire struct {
	event   *pluginv1.TaskEvent
	drift   *pluginv1.DiffFragment
	changed bool
}

// lineToWire maps one raw tofu -json line. seq is the deterministic per-Run
// counter (retry re-publishes dedup-identically). Non-JSON lines become a raw
// info event (never dropped).
func lineToWire(seq int64, at *timestamppb.Timestamp, raw []byte) lineWire {
	var m tofuMsg
	if err := json.Unmarshal(raw, &m); err != nil || (m.Type == "" && m.Message == "") {
		return lineWire{event: &pluginv1.TaskEvent{
			Level: pluginv1.TaskEvent_LEVEL_INFO, Message: strings.TrimSpace(string(raw)),
			At: at, Fields: map[string]string{"kind": "raw"},
		}}
	}
	fields := map[string]string{"kind": "tofu", "type": m.Type}
	level := levelOf(m.Level)
	msg := m.Message

	if m.Diagnostic != nil {
		fields["kind"] = "diagnostic"
		fields["severity"] = m.Diagnostic.Severity
		fields["summary"] = m.Diagnostic.Summary
		if m.Diagnostic.Detail != "" {
			fields["detail"] = m.Diagnostic.Detail
		}
		if m.Diagnostic.Severity == "error" {
			level = pluginv1.TaskEvent_LEVEL_ERROR
		}
	}
	if m.Changes != nil {
		fields["add"] = fmt.Sprint(m.Changes.Add)
		fields["change"] = fmt.Sprint(m.Changes.Change)
		fields["remove"] = fmt.Sprint(m.Changes.Remove)
	}

	w := lineWire{event: &pluginv1.TaskEvent{Level: level, Message: msg, At: at, Fields: fields}}

	// Drift capture (ADR-0019): a plan that WOULD change is "changed" in check
	// semantics — addresses + counts only, never planned values (those stay in the
	// redacted plan-json, §2.5).
	switch {
	case m.Type == "change_summary" && m.Changes != nil && m.Changes.Operation == "plan" &&
		m.Changes.Add+m.Changes.Change+m.Changes.Remove > 0:
		w.changed = true
		detail, _ := json.Marshal(map[string]any{"add": m.Changes.Add, "change": m.Changes.Change, "remove": m.Changes.Remove})
		w.drift = &pluginv1.DiffFragment{Detail: &pluginv1.Payload{Bytes: detail}}
	case (m.Type == "planned_change" || m.Type == "resource_drift") && m.Change != nil:
		frag := map[string]any{"address": m.Change.Resource.Addr, "action": m.Change.Action}
		if m.Type == "resource_drift" {
			frag["drift"] = true
		}
		detail, _ := json.Marshal(frag)
		w.drift = &pluginv1.DiffFragment{Detail: &pluginv1.Payload{Bytes: detail}}
	}
	return w
}

func levelOf(tofuLevel string) pluginv1.TaskEvent_Level {
	switch tofuLevel {
	case "error":
		return pluginv1.TaskEvent_LEVEL_ERROR
	case "warn":
		return pluginv1.TaskEvent_LEVEL_WARN
	default:
		return pluginv1.TaskEvent_LEVEL_INFO
	}
}

// tofuOutput is one entry of `tofu output -json`.
type tofuOutput struct {
	Sensitive bool            `json:"sensitive"`
	Type      json.RawMessage `json:"type"`
	Value     json.RawMessage `json:"value"`
}

// outputsToWire parses `tofu output -json`: the reserved stratt_entities output
// becomes governed write-back ObservedEntity proposals (the CORE validates them
// against the pinned rung-1 Contract + governs identity/labels — the plugin only
// proposes, §1.2), and the whole document derives the rung-2 outputs schema the
// core recomputes + pins (§2.2). A stratt.* label prefix is reserved (the platform
// stamps stratt.workspace) — a collision fails the Apply visibly, never silently.
func outputsToWire(raw []byte) ([]*pluginv1.ObservedEntity, []byte, error) {
	if len(raw) == 0 {
		return nil, nil, nil
	}
	var outputs map[string]tofuOutput
	if err := json.Unmarshal(raw, &outputs); err != nil {
		return nil, nil, fmt.Errorf("decode outputs: %w", err)
	}
	var entities []*pluginv1.ObservedEntity
	if ent, ok := outputs["stratt_entities"]; ok {
		var wire []struct {
			Kind         string            `json:"kind"`
			IdentityKeys map[string]string `json:"identityKeys"`
			Labels       map[string]string `json:"labels"`
		}
		if err := json.Unmarshal(ent.Value, &wire); err != nil {
			return nil, nil, fmt.Errorf("decode stratt_entities: %w", err)
		}
		for i, w := range wire {
			for k := range w.Labels {
				if strings.HasPrefix(k, "stratt.") {
					return nil, nil, fmt.Errorf("stratt_entities[%d]: label %q uses the reserved stratt.* prefix", i, k)
				}
			}
			entities = append(entities, &pluginv1.ObservedEntity{
				Kind: w.Kind, IdentityKeys: w.IdentityKeys, Labels: w.Labels,
			})
		}
	}
	types := make(map[string]tofuOutputType, len(outputs))
	for name, o := range outputs {
		types[name] = tofuOutputType{Type: o.Type, Sensitive: o.Sensitive}
	}
	schema, err := deriveOutputsSchema(types)
	if err != nil {
		return nil, nil, err
	}
	return entities, schema, nil
}
