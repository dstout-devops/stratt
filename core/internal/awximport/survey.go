package awximport

import (
	"encoding/json"

	"github.com/dstout-devops/stratt/core/internal/awximport/awx"
)

// mapSurvey transforms an AWX survey into an input Contract: a JSON Schema
// 2020-12 document. UI hints ride as x- extensions; a password question is
// marked x-stratt-sensitive. Registration/pinning (rung-2, ADR-0022) happens at
// apply time — the importer writes the document and records the pin name in the
// report.
//
// NOTE (flagged, not faked): types.Step cannot yet reference an arbitrary input
// Contract, so this document is emitted + reviewable now; enforcing it against
// launch params is the follow-up Step `inputContract` binding.
func mapSurvey(jt awx.JobTemplate, spec awx.SurveySpec, r *report) (string, error) {
	props := map[string]any{}
	var required []string

	for _, q := range spec.Spec {
		if q.Variable == "" {
			continue
		}
		schema := map[string]any{}
		switch q.Type {
		case "text", "textarea":
			schema["type"] = "string"
		case "password":
			schema["type"] = "string"
			schema["x-stratt-sensitive"] = true
		case "integer":
			schema["type"] = "integer"
		case "float":
			schema["type"] = "number"
		case "multiplechoice":
			schema["type"] = "string"
			if choices := decodeChoices(q.Choices); len(choices) > 0 {
				schema["enum"] = choices
			}
		case "multiselect":
			items := map[string]any{"type": "string"}
			if choices := decodeChoices(q.Choices); len(choices) > 0 {
				items["enum"] = choices
			}
			schema["type"] = "array"
			schema["items"] = items
		default:
			schema["type"] = "string"
			r.note("Input Contract for Workflow (was: survey on job template %q): question %q has unmapped type %q — defaulted to string.", jt.Name, q.Variable, q.Type)
		}

		if q.QuestionName != "" {
			schema["title"] = q.QuestionName
		}
		if q.QuestionDescription != "" {
			schema["description"] = q.QuestionDescription
		}
		if len(q.Default) > 0 && string(q.Default) != "null" {
			var def any
			if json.Unmarshal(q.Default, &def) == nil {
				schema["default"] = def
			}
		}
		applyBounds(schema, q)
		if q.Required {
			required = append(required, q.Variable)
		}
		props[q.Variable] = schema
	}

	doc := map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"$id":                  "stratt:contract:surveys/" + slug(jt.Name) + ".input:v1",
		"title":                spec.Name,
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if spec.Description != "" {
		doc["description"] = spec.Description
	}
	if len(required) > 0 {
		doc["required"] = required
	}

	r.note("Input Contract `surveys/%s.input` (was: survey on job template %q): emitted as JSON Schema. Pin it (rung-2) and bind it to the Workflow's Step once Step `inputContract` binding ships — not yet enforced against launch params.", slug(jt.Name), jt.Name)

	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", mapErr("survey", jt.Name, err)
	}
	return string(b) + "\n", nil
}

// applyBounds maps min/max onto string length or numeric range by question type.
func applyBounds(schema map[string]any, q awx.SurveyQuestion) {
	switch q.Type {
	case "text", "textarea", "password":
		if q.Min != nil {
			schema["minLength"] = *q.Min
		}
		if q.Max != nil {
			schema["maxLength"] = *q.Max
		}
	case "integer", "float":
		if q.Min != nil {
			schema["minimum"] = *q.Min
		}
		if q.Max != nil {
			schema["maximum"] = *q.Max
		}
	}
}

// decodeChoices accepts AWX's choices as either a []string or a newline-joined
// string (older surveys), returning the option list.
func decodeChoices(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		var out []string
		for _, line := range splitLines(s) {
			if line != "" {
				out = append(out, line)
			}
		}
		return out
	}
	return nil
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '\n' || r == '\r' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	out = append(out, cur)
	return out
}
