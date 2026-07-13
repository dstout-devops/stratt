package awxfacade

import (
	"encoding/json"
	"errors"
	"net/http"

	yaml "go.yaml.in/yaml/v3"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/orchestrate"
)

// launchBody is the subset of an AWX launch request the façade honors. Fields
// not honored in v1 are echoed back in ignored_fields (AWX-legal, honest §1.8).
type launchBody struct {
	ExtraVars   json.RawMessage `json:"extra_vars"` // object OR yaml/json string
	ScmBranch   string          `json:"scm_branch"`
	Limit       string          `json:"limit"`
	Inventory   *int64          `json:"inventory"`
	Credentials json.RawMessage `json:"credentials"`
}

// launch: POST /api/v2/job_templates/{id}/launch/ — resolve the Workflow's
// single Step, merge AWX extra_vars into params.extraVars, and launch a Run via
// the shared native launch path.
func (f *Facade) launch(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		awxErr(w, http.StatusNotFound, "Not found.")
		return
	}
	wf, step, ok := f.resolveJobTemplate(r, id)
	if !ok {
		awxErr(w, http.StatusNotFound, "Not found.")
		return
	}
	_ = wf

	var body launchBody
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			awxErr(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
	}

	extra, err := parseExtraVars(body.ExtraVars)
	if err != nil {
		awxErr(w, http.StatusBadRequest, "invalid extra_vars: "+err.Error())
		return
	}

	// Merge onto the Step's declared params (launch-time values win).
	params := cloneParams(step.Params)
	if len(extra) > 0 {
		merged := map[string]any{}
		if existing, ok := params["extraVars"].(map[string]any); ok {
			for k, v := range existing {
				merged[k] = v
			}
		}
		for k, v := range extra {
			merged[k] = v
		}
		params["extraVars"] = merged
	}
	if body.ScmBranch != "" {
		if scm, ok := params["scm"].(map[string]any); ok {
			scm["ref"] = body.ScmBranch
		}
	}

	raw, err := json.Marshal(params)
	if err != nil {
		awxErr(w, http.StatusBadRequest, err.Error())
		return
	}

	id2, _, _ := principal(r)
	if !f.requireRunner(r.Context(), w, id2, step.ViewName) {
		return
	}
	run, err := orchestrate.LaunchRun(r.Context(), orchestrate.LaunchDeps{Store: f.cfg.Store, Temporal: f.cfg.Temporal},
		orchestrate.LaunchParams{
			ViewName:       step.ViewName,
			Actuator:       step.Actuator,
			Params:         raw,
			CredentialRefs: step.CredentialRefs,
			Principal:      id2,
		})
	if err != nil {
		// Surface the native detail (§1.8), with a faithful status: a missing
		// View is 404, a contract violation on the resolved params is 400, an
		// infra/start failure is 500.
		switch {
		case errors.Is(err, graph.ErrNotFound):
			awxErr(w, http.StatusNotFound, err.Error())
		case errors.Is(err, orchestrate.ErrStartWorkflow):
			awxErr(w, http.StatusInternalServerError, err.Error())
		default:
			awxErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}

	jobID := awxID(run.ID)
	writeJSON(w, http.StatusCreated, map[string]any{
		"job":            jobID,
		"id":             jobID,
		"type":           "job",
		"status":         "pending",
		"url":            jt("/api/v2/jobs/%d/", jobID),
		"ignored_fields": ignoredFields(body),
	})
}

// parseExtraVars accepts AWX extra_vars as a JSON object or a YAML/JSON string.
func parseExtraVars(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	// Object form.
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj, nil
	}
	// String form (AWX allows an extra_vars string of YAML or JSON).
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	if s == "" {
		return nil, nil
	}
	out := map[string]any{}
	if err := yaml.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// cloneParams deep-enough-copies a Step's params so a launch never mutates the
// declared Workflow in memory.
func cloneParams(p map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range p {
		if m, ok := v.(map[string]any); ok {
			mm := map[string]any{}
			for k2, v2 := range m {
				mm[k2] = v2
			}
			out[k] = mm
			continue
		}
		out[k] = v
	}
	return out
}

// ignoredFields reports launch inputs the façade did not honor in v1.
func ignoredFields(b launchBody) map[string]any {
	out := map[string]any{}
	if b.Limit != "" {
		out["limit"] = b.Limit
	}
	if b.Inventory != nil {
		out["inventory"] = *b.Inventory
	}
	if len(b.Credentials) > 0 && string(b.Credentials) != "null" {
		out["credentials"] = json.RawMessage(b.Credentials)
	}
	return out
}
