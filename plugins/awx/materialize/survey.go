package materialize

import (
	"encoding/json"
	"sort"

	awx "github.com/dstout-devops/stratt/plugins/awx/controller"
)

// mapSurvey transforms an AWX survey into the imported Workflow's declared launch
// interface: a JSON Schema 2020-12 object document, emitted as `Workflow.inputs`
// (ADR-0118 D2). UI hints ride as x- extensions. A **password** question is refused
// rather than typed — see the §2.5 block below.
//
// It used to be written to a detached `contracts/<name>.survey.schema.json` instead,
// because ADR-0025 found no seam to bind it to: `types.Step` could not reference an
// arbitrary input Contract, so the document was "emitted + reviewable but not enforced"
// and the binding was deferred as follow-up (c). ADR-0118 D2 is that seam. A Workflow's
// `inputs` is validated at ONE chokepoint below all four launch paths
// (contract.ResolveLaunchInputs), so an imported survey is now enforced against launch
// params on the API, MCP, and both Trigger doors identically (§1.6) — and because the
// schema is closed, an answer to a question the survey does not ask is rejected rather
// than silently dropped.
//
// The detached file is NOT also emitted. Two copies of one survey would be two
// authorities for one fact (§1.2), and the copy nothing reads is the one that rots.
//
// Two deliberate departures from the old document:
//   - No `$id`. That named a registry Contract to be pinned at rung-2, and a Workflow's
//     inputs are git-declared estate, carrying no pin and no registry row
//     (core/internal/contract/workflowinputs.go says so at length). An `$id` here would
//     advertise a hash-verification that does not happen, which is worse than none (§1.8).
//   - `additionalProperties: false` is now load-bearing rather than tidy: CompileInputSchema
//     REQUIRES it, so a survey that failed to close itself would fail the import's own parse.
func mapSurvey(jt awx.JobTemplate, spec awx.SurveySpec, wfName string, r *report) (map[string]any, []string, error) {
	props := map[string]any{}
	var required []string

	for _, q := range spec.Spec {
		if q.Variable == "" {
			continue
		}
		if q.Type == "password" {
			// A password question is SECRET MATERIAL, and binding it would carry that
			// material through launch params — recorded on the Run, in the audit stream,
			// and handed to --extra-vars, whose own Contract says it "never carries secret
			// material (§2.5); credentials stay on credentialRefs". So it is not imported
			// as an input at all, and the bundle BLOCKS until it is re-brokered, exactly as
			// an imported AWX credential does.
			//
			// This only became a live question when surveys started being enforced. While
			// the document was detached and read by nothing, marking the property
			// x-stratt-sensitive was harmless; the moment it became a real launch input, the
			// marker would have been a note attached to a §2.5 violation.
			r.block("Workflow %q (was: survey on job template %q): question %q is a password — secret material must be brokered, never passed as a launch input (§2.5). It is NOT imported as an input; declare a CredentialRef and add it to the Step's credentialRefs before apply.", wfName, jt.Name, q.Variable)
			continue
		}
		schema := map[string]any{}
		switch q.Type {
		case "text", "textarea":
			schema["type"] = "string"
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

	if len(props) == 0 {
		// Every question was skipped — no `variable`, or all of them passwords (which are
		// blocked above, not imported). Emitting `inputs: {}` instead would be worse than
		// nothing: a closed empty schema means "accepts no inputs", which is a claim about
		// the survey rather than an admission that none of it was importable.
		r.note("Survey on job template %q contributed no importable questions — the Workflow declares no `inputs`. Check the blocking entries above for password questions that must be re-brokered.", jt.Name)
		return nil, nil, nil
	}

	doc := map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"title":                spec.Name,
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if spec.Description != "" {
		doc["description"] = spec.Description
	}
	if len(required) > 0 {
		sort.Strings(required)
		doc["required"] = required
	}

	vars := make([]string, 0, len(props))
	for v := range props {
		vars = append(vars, v)
	}
	sort.Strings(vars) // deterministic emission — the bundle is reviewed as a diff

	r.note("Workflow %q declares the survey from job template %q as its `inputs` (%d question(s)): launch params are validated against it on every launch path, and each answer is bound into the Step's extraVars.", wfName, jt.Name, len(vars))

	return doc, vars, nil
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
