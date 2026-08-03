package awxfacade

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/dstout-devops/stratt/types"
)

// ── ask_*_on_launch, DERIVED (ADR-0160 D2) ───────────────────────────────────────────────────────
//
// AWX tooling READS these booleans to decide what to prompt for. They were hardcoded — three of them,
// two always false — so a migrated template lost its prompts even where the mechanism worked. That is
// a §7.6 strangler-fig failure: the front door lying about the building behind it.
//
// THE MECHANISM WAS ALREADY THERE. `ResolveStepParams` substitutes the `{{.launch.x}}` namespace into
// a Step's params, and every promptable ansible knob is already a param of ansible.input.v8. So a
// Workflow that declares an input and binds it into `limit` HAS a promptable limit; nothing new is
// needed to make it true, only to stop under-reporting it.
//
// BOTH HALVES ARE REQUIRED, because either alone is a lie:
//   - a binding with no declared input is a token nothing can fill — the launch resolver rejects an
//     answer to a question the Workflow does not ask (ADR-0118 D2), so the prompt would 400;
//   - a declared input nothing binds changes no behaviour, and advertising it would invite a caller
//     to supply a value that reaches nothing.
//
// DERIVED, NEVER DECLARED. A `promptable:` list on the Step would be a second home for a fact the
// binding already states, and the two would drift (§2.4). The binding IS the declaration.

// promptableParams maps a Step param PATH (dotted, for nested params) to the AWX field name whose
// `ask_<name>_on_launch` boolean it drives.
//
// Only fields whose task Stratt can actually perform appear here. `job_type` is absent because
// dry-run is a property of the Run, not a param (ADR-0117 D2) — the ownership moved deliberately and
// reporting a prompt for a param that does not exist would be the same lie in the other direction.
var promptableParams = map[string]string{
	"limit":     "limit",
	"tags":      "tags",
	"skipTags":  "skip_tags",
	"diff":      "diff_mode",
	"verbosity": "verbosity",
	"forks":     "forks",
	"timeout":   "timeout",
	"scm":       "scm_branch", // params.scm.ref, bound anywhere in the subtree
}

// `variables` is NOT in the map above, and the difference is real rather than an oversight.
//
// Every field in that map names ONE param, and a value that reaches no param changes nothing — so
// the prompt is only honest if something binds it. `variables` is the SURVEY: ADR-0118 D2 makes a
// declared `inputs` interface the survey itself, and the façade's launch path accepts a caller's
// extra_vars as its ANSWERS whenever one is declared, whether or not a Step happens to bind a given
// question. The truth condition is "the door accepts them", which is `len(wf.Inputs) > 0`.
//
// `credential` is deliberately absent too, and stays false until ADR-0160 D4 ships the declared
// permitted set. A launch-supplied credentialRef today would bypass the §2.5 use-check, which is
// authorized against the Step's DECLARED refs — advertising a prompt for it would invite exactly
// the escalation D4 exists to make safe.

// launchToken matches `{{.launch.<name>}}` — the ONLY binding namespace that makes a value
// launch-supplied. `{{.event.x}}` and `{{.steps.x}}` are bound from elsewhere and are not prompts.
var launchToken = regexp.MustCompile(`\{\{\s*\.launch\.([A-Za-z0-9_]+)`)

// declaredInputs reads the property names out of a Workflow's declared `inputs` schema. Nil inputs
// (a Workflow that takes none) yields an empty set, which makes every prompt false — correct: with
// no declared question there is nothing a caller may answer.
func declaredInputs(raw json.RawMessage) map[string]bool {
	out := map[string]bool{}
	if len(raw) == 0 {
		return out
	}
	var doc struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return out
	}
	for k := range doc.Properties {
		out[k] = true
	}
	return out
}

// launchBoundInputs walks a params document and returns the declared input names bound anywhere
// inside it, keyed by the top-level param path they were found under.
//
// The WHOLE subtree counts for a param: `scm.ref` and `connection.credentialRef` are nested, and a
// caller prompting for a branch is prompting for `scm_branch` regardless of how deep the token sits.
func launchBoundInputs(params map[string]any) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for key, v := range params {
		names := map[string]bool{}
		collectLaunchTokens(v, names)
		if len(names) > 0 {
			out[key] = names
		}
	}
	return out
}

func collectLaunchTokens(v any, into map[string]bool) {
	switch t := v.(type) {
	case string:
		for _, m := range launchToken.FindAllStringSubmatch(t, -1) {
			into[m[1]] = true
		}
	case map[string]any:
		for _, e := range t {
			collectLaunchTokens(e, into)
		}
	case []any:
		for _, e := range t {
			collectLaunchTokens(e, into)
		}
	}
}

// askFields renders the `ask_*_on_launch` booleans for one Step of one Workflow (ADR-0160 D2).
//
// EVERY field AWX defines is emitted, including the false ones. A migrating client reads the absence
// of a key as "this server is old" rather than "this prompt is off", and the two need to be
// distinguishable — which is the same reason the manifest in ADR-0159 records an empty python
// section rather than none.
// acceptsVars is passed rather than computed, because THE TWO SURFACES GENUINELY DISAGREE TODAY and
// this function must not quietly pick a winner:
//
//   - job_templates: `resolveLaunchParams` MERGES a caller's extra_vars onto params.extraVars when
//     the Workflow declares no inputs — "AWX's own untyped ask_variables_on_launch behaviour, which
//     is what this compat surface exists to emulate". The door always accepts them, so it is always
//     true.
//   - workflow_job_templates: extra_vars are the SURVEY's answers, and a Workflow declaring no
//     interface advertises a door ResolveLaunchInputs then slams. False without `inputs`.
//
// Both are shipped and both are tested. Unifying them is a behaviour change on a compat surface and
// belongs in its own decision, not smuggled in beside a derivation change — booked, and the
// disagreement is written down here rather than left for the next reader to rediscover.
func askFields(wf types.Workflow, step types.Step, acceptsVars bool) map[string]any {
	declared := declaredInputs(wf.Inputs)
	bound := launchBoundInputs(step.Params)

	on := map[string]bool{}
	on["variables"] = acceptsVars
	for paramKey, names := range bound {
		field, ok := promptableParams[paramKey]
		if !ok {
			continue
		}
		for n := range names {
			if declared[n] {
				on[field] = true
				break
			}
		}
	}
	// A View supplied at launch is a capability of the DOOR, not of a binding (ADR-0160 D3): any
	// Workflow whose actuation Steps inherit their View can be pointed at a different one.
	on["inventory"] = inheritsView(wf)

	out := map[string]any{}
	for _, f := range []string{
		"variables", "limit", "inventory", "credential", "scm_branch", "job_type",
		"tags", "skip_tags", "diff_mode", "verbosity", "forks", "timeout",
		"execution_environment", "instance_groups", "labels", "job_slice_count",
	} {
		out[fmt.Sprintf("ask_%s_on_launch", f)] = on[f]
	}
	return out
}

// inheritsView reports whether this Workflow's actuation Steps take their View from the launch
// rather than naming their own — the shape a supplied `viewName` can point somewhere else (D3).
// A Step that names its own View is pinned by the estate and is not a prompt.
func inheritsView(wf types.Workflow) bool {
	for _, st := range wf.Steps {
		if st.IsActuation() && strings.TrimSpace(st.ViewName) == "" {
			return true
		}
	}
	return false
}

// askFieldsForWorkflow is askFields for a whole DAG: the UNION across its Steps.
//
// Union, because AWX exposes one set of prompts per workflow_job_template and a caller supplying an
// answer expects it to reach wherever it is bound. A Workflow whose `deliver` Step binds `limit`
// prompts for a limit even though `gather` does not — the answer resolves into the Step that asked
// for it and is simply absent from the others, which is exactly what the launch resolver already
// does for variables (ADR-0118 D2).
func askFieldsForWorkflow(wf types.Workflow) map[string]any {
	out := map[string]any{}
	for _, st := range wf.Steps {
		for k, v := range askFields(wf, st, len(wf.Inputs) > 0) {
			if b, _ := v.(bool); b {
				out[k] = true
			} else if _, seen := out[k]; !seen {
				out[k] = false
			}
		}
	}
	if len(out) == 0 {
		// A Workflow with no Steps still has to publish the full key set, so a client can tell
		// "prompt off" from "server too old to say".
		return askFields(wf, types.Step{}, len(wf.Inputs) > 0)
	}
	return out
}
