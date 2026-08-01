package awxfacade

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/orchestrate"
	"github.com/dstout-devops/stratt/types"
)

// ── workflow_job_templates: the AWX WFJT resource, served from multi-Step Workflows ─────────────
//
// job_templates presents SINGLE-Step, Gate-free Workflows (mappers.singleActuationStep). Everything
// else — every DAG, every gated Workflow, every Workflow with a policy checkpoint or a nested child
// — had NO representation at all: `listJobTemplates` skipped it with a comment calling it a
// fast-follow. An AWX client pointed at Stratt could not see, let alone launch, the Workflows the
// estate actually runs. That is the strangler-fig front door failing at exactly the shapes an
// adopter migrates FOR.
//
// The partition is exact and total: a Workflow is a job_template iff singleActuationStep accepts it,
// and a workflow_job_template otherwise. No Workflow appears in both families and none appears in
// neither — TestEveryWorkflowLandsInExactlyOneFamily pins it, because a Workflow that fell through
// the gap would be invisible rather than wrong, which is the harder failure to notice.
//
// ── WHAT A NODE POINTS AT, AND WHEN IT POINTS AT NOTHING ────────────────────────────────────────
// An AWX workflow node carries `unified_job_template`: the job_template/WFJT that node runs. A
// Stratt Step is NOT independently launchable — it is a node of this DAG and has no job_template of
// its own — so pointing at a synthesized id would be a DANGLING REFERENCE: the client would fetch
// /api/v2/job_templates/<id>/ and get a 404, or worse, a hash collision with an unrelated Workflow.
//
// So `unified_job_template` is null for an ordinary Step, and non-null in exactly one case: a NESTED
// Workflow Step (ADR-0139), which really does name another declared Workflow that really does have
// its own id in one of the two families. That is a true edge, and it is the §1.8 descent link an
// operator follows from the parent DAG into the child.
//
// The Step's actual shape rides in `summary_fields.stratt` — a clearly namespaced ADDITION, never a
// reinterpretation of an AWX field. The rule this package works by: an AWX field either carries its
// AWX meaning faithfully or is absent, and Stratt truth with no AWX equivalent goes somewhere no
// client will mistake for AWX.
//
// READ-ONLY except launch, like the rest of the façade: the DAG is a declaration reconciled from Git.

// stepShape names what a Step IS, for the descent block. AWX has a node type for two of these
// (a job and an approval) and nothing for the rest; naming them plainly beats flattening them into
// "job", which would show an operator a DAG that does not match the one that runs.
func stepShape(s types.Step) string {
	switch {
	case s.Gate != nil:
		return "gate" // AWX's nearest concept is a workflow_approval node
	case s.Policy != nil:
		return "policy"
	case s.Workflow != "" || s.WorkflowCapability != "":
		return "nested-workflow"
	case s.Action != "" || s.ActionCapability != "":
		return "action"
	default:
		return "actuation"
	}
}

// nodeID synthesizes the AWX node id from the Step's fully-qualified name. Qualified by the
// Workflow, so two Workflows with a Step called "apply" do not collide.
func nodeID(workflow, step string) int64 { return awxID(workflow + "/" + step) }

// workflowToWFJT renders a multi-Step Workflow as an AWX workflow_job_template.
func workflowToWFJT(wf types.Workflow) map[string]any {
	id := awxID(wf.Name)
	// A WFJT asks for variables on launch only when the Workflow declares an `inputs` interface.
	// Saying `true` unconditionally would invite a caller to send extra_vars that
	// contract.ResolveLaunchInputs then refuses — advertising a door that is closed.
	asksVars := len(wf.Inputs) > 0
	out := map[string]any{
		"id":                      id,
		"type":                    "workflow_job_template",
		"name":                    wf.Name,
		"description":             fmt.Sprintf("Stratt Workflow %q (%d steps)", wf.Name, len(wf.Steps)),
		"inventory":               nil, // a DAG has no single View; each actuation Step names its own
		"ask_variables_on_launch": asksVars,
		"ask_limit_on_launch":     false,
		"ask_inventory_on_launch": false,
		"url":                     jt("/api/v2/workflow_job_templates/%d/", id),
		"related": map[string]any{
			"launch":         jt("/api/v2/workflow_job_templates/%d/launch/", id),
			"workflow_nodes": jt("/api/v2/workflow_job_templates/%d/workflow_nodes/", id),
		},
		"summary_fields": map[string]any{
			"user_capabilities": map[string]bool{"start": true, "edit": false, "delete": false},
		},
	}
	// The launch interface IS the survey on this surface (ADR-0118 D2 — where an imported AWX
	// survey lands). Exposing the schema lets a client render the same form the native door does.
	if asksVars {
		out["survey_enabled"] = true
		out["extra_vars"] = json.RawMessage(wf.Inputs)
	} else {
		out["survey_enabled"] = false
	}
	return out
}

// workflowNodes renders a Workflow's Steps as AWX workflow nodes, inverting Stratt's `needs` edges
// (which point BACKWARD, at prerequisites) into AWX's success/failure/always lists (which point
// FORWARD, at successors). Same DAG, opposite arrow direction — get this wrong and a client draws a
// graph that runs in reverse.
func workflowNodes(wf types.Workflow) []any {
	wfID := awxID(wf.Name)
	// Pre-create every node so an edge can reference a Step declared later in the file.
	nodes := make(map[string]map[string]any, len(wf.Steps))
	order := make([]string, 0, len(wf.Steps))
	for _, s := range wf.Steps {
		id := nodeID(wf.Name, s.Name)
		n := map[string]any{
			"id":                    id,
			"type":                  "workflow_job_template_node",
			"url":                   jt("/api/v2/workflow_job_template_nodes/%d/", id),
			"workflow_job_template": wfID,
			"identifier":            s.Name,
			"unified_job_template":  nil,
			"success_nodes":         []int64{},
			"failure_nodes":         []int64{},
			"always_nodes":          []int64{},
			"extra_data":            map[string]any{},
			"summary_fields": map[string]any{
				"stratt": strattStepFields(s),
			},
		}
		// The one true edge to another AWX object: a nested Workflow Step names a declared
		// Workflow, which has a real id in one of the two families.
		if s.Workflow != "" {
			n["unified_job_template"] = awxID(s.Workflow)
		}
		nodes[s.Name] = n
		order = append(order, s.Name)
	}
	for _, s := range wf.Steps {
		key := "success_nodes"
		switch s.When {
		case types.WhenFailure:
			key = "failure_nodes"
		case types.WhenAlways:
			key = "always_nodes"
		}
		for _, need := range s.Needs {
			parent, ok := nodes[need]
			if !ok {
				continue // a dangling `needs` fails at load; nothing to draw here
			}
			parent[key] = append(parent[key].([]int64), nodeID(wf.Name, s.Name))
		}
	}
	out := make([]any, 0, len(order))
	for _, name := range order {
		out = append(out, nodes[name])
	}
	return out
}

// strattStepFields is the descent block: what this node actually is, in Stratt's own vocabulary.
// Namespaced under `stratt` so no client can mistake it for an AWX field (§1.8 — the mechanism is
// hidden, the diagnosis never is).
func strattStepFields(s types.Step) map[string]any {
	f := map[string]any{"step": s.Name, "shape": stepShape(s)}
	for k, v := range map[string]string{
		"view":                s.ViewName,
		"actuator":            s.Actuator,
		"action":              s.Action,
		"action_capability":   s.ActionCapability,
		"workflow":            s.Workflow,
		"workflow_capability": s.WorkflowCapability,
		"when":                s.When,
	} {
		if v != "" {
			f[k] = v
		}
	}
	if s.Gate != nil {
		f["approvers"] = map[string]any{
			"principals": s.Gate.Approvers.Principals,
			"teams":      s.Gate.Approvers.Teams,
			"threshold":  s.Gate.Threshold,
		}
	}
	if s.Policy != nil {
		f["controls"] = len(s.Policy.Controls)
	}
	return f
}

// isWFJT reports whether a Workflow is presented as a workflow_job_template — the exact complement
// of the job_template family.
func isWFJT(wf types.Workflow) bool {
	_, single := singleActuationStep(wf)
	return !single
}

// listWorkflowJobTemplates: GET /api/v2/workflow_job_templates/.
func (f *Facade) listWorkflowJobTemplates(w http.ResponseWriter, r *http.Request) {
	wfs, err := f.cfg.Store.ListWorkflows(r.Context())
	if err != nil {
		awxErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]named, 0, len(wfs))
	for _, wf := range wfs {
		if !isWFJT(wf) {
			continue // single-Step Workflows are job_templates
		}
		items = append(items, named{id: awxID(wf.Name), name: wf.Name, obj: workflowToWFJT(wf)})
	}
	writeJSON(w, http.StatusOK, paginate(r, items))
}

// resolveWFJT reverse-matches an AWX WFJT id to a multi-Step Workflow.
func (f *Facade) resolveWFJT(r *http.Request, id int64) (types.Workflow, bool) {
	wfs, err := f.cfg.Store.ListWorkflows(r.Context())
	if err != nil {
		return types.Workflow{}, false
	}
	for _, wf := range wfs {
		if awxID(wf.Name) == id && isWFJT(wf) {
			return wf, true
		}
	}
	return types.Workflow{}, false
}

// getWorkflowJobTemplate: GET /api/v2/workflow_job_templates/{id}/.
func (f *Facade) getWorkflowJobTemplate(w http.ResponseWriter, r *http.Request) {
	wf, ok := f.wfjtByPathID(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, workflowToWFJT(wf))
}

// getWorkflowNodes: GET /api/v2/workflow_job_templates/{id}/workflow_nodes/.
func (f *Facade) getWorkflowNodes(w http.ResponseWriter, r *http.Request) {
	wf, ok := f.wfjtByPathID(w, r)
	if !ok {
		return
	}
	nodes := workflowNodes(wf)
	writeJSON(w, http.StatusOK, envelope{Count: len(nodes), Results: nodes})
}

func (f *Facade) wfjtByPathID(w http.ResponseWriter, r *http.Request) (types.Workflow, bool) {
	id, ok := pathID(r)
	if !ok {
		awxErr(w, http.StatusNotFound, "Not found.")
		return types.Workflow{}, false
	}
	wf, ok := f.resolveWFJT(r, id)
	if !ok {
		awxErr(w, http.StatusNotFound, "Not found.")
		return types.Workflow{}, false
	}
	return wf, true
}

// launchWFJT: POST /api/v2/workflow_job_templates/{id}/launch/.
//
// It calls orchestrate.LaunchWorkflowRun — the SAME function POST /api/v1/workflows/{name}/runs and
// the remediation door call. The alternative was a private copy of the sequence here, which is the
// second launch path that §1.6 asymmetry keeps being caused by; the job_template launch beside this
// one already reuses LaunchRun for the same reason.
//
// extra_vars are the caller's LAUNCH INPUTS, not a merge into some Step's extraVars: a DAG has no
// single Step to merge into, and inventing one would pick a Step for the caller. Routing them
// through the declared interface means a Workflow that declares none REFUSES them with a message
// naming the keys, rather than accepting and dropping them.
func (f *Facade) launchWFJT(w http.ResponseWriter, r *http.Request) {
	wf, ok := f.wfjtByPathID(w, r)
	if !ok {
		return
	}
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

	principal, _, _ := principal(r)
	// EVERY actuation Step's View, not just the first — the compat surface must not be a weaker
	// authz path than the native one (§1.6, and api.authorizeLaunch is the shape being matched).
	for _, st := range wf.Steps {
		if !st.IsActuation() {
			continue
		}
		if st.ViewName == "" {
			// A direct launch supplies no View to inherit. Refuse rather than run the Step
			// against nothing — an omitted field must not become a bypassed gate.
			awxErr(w, http.StatusBadRequest, fmt.Sprintf(
				"workflow %s step %q converges a View but names none, and a launch supplies none to "+
					"inherit — only a Finding remediation can supply it", wf.Name, st.Name))
			return
		}
		if !f.requireRunner(r.Context(), w, principal, st.ViewName) {
			return
		}
	}

	wr, err := orchestrate.LaunchWorkflowRun(r.Context(),
		orchestrate.LaunchDeps{Store: f.cfg.Store, Temporal: f.cfg.Temporal},
		orchestrate.WorkflowLaunchParams{Workflow: wf, Principal: principal, Inputs: extra})
	if err != nil {
		switch {
		case errors.Is(err, orchestrate.ErrLaunchInput):
			awxErr(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, graph.ErrNotFound):
			awxErr(w, http.StatusNotFound, err.Error())
		case errors.Is(err, orchestrate.ErrStartWorkflow):
			awxErr(w, http.StatusInternalServerError, err.Error())
		default:
			awxErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	jobID := awxID(wr.ID)
	writeJSON(w, http.StatusCreated, map[string]any{
		"workflow_job":   jobID,
		"id":             jobID,
		"type":           "workflow_job",
		"status":         "pending",
		"url":            jt("/api/v2/workflow_jobs/%d/", jobID),
		"ignored_fields": ignoredFields(body),
	})
}

// ── workflow_jobs: executions of a WFJT ─────────────────────────────────────────────────────────
//
// Shipped WITH the launch rather than after it, because a launch a client cannot poll is a §1.8
// failure: awxkit's `--monitor` follows the returned url, and a 404 there reads as "the thing you
// just started does not exist".

// workflowRunToJob renders a WorkflowRun as an AWX workflow_job.
func workflowRunToJob(wr types.WorkflowRun) map[string]any {
	id := awxID(wr.ID)
	var finished any
	elapsed := 0.0
	if wr.FinishedAt != nil {
		finished = wr.FinishedAt.UTC().Format(time.RFC3339)
		elapsed = wr.FinishedAt.Sub(wr.StartedAt).Seconds()
	}
	out := map[string]any{
		"id":                    id,
		"workflow_job":          id,
		"type":                  "workflow_job",
		"name":                  wr.WorkflowName,
		"status":                mapStatus(wr.Status),
		"failed":                wr.Status == types.RunFailed || wr.Status == types.RunPartial,
		"started":               wr.StartedAt.UTC().Format(time.RFC3339),
		"finished":              finished,
		"elapsed":               elapsed,
		"workflow_job_template": awxID(wr.WorkflowName),
		"url":                   jt("/api/v2/workflow_jobs/%d/", id),
		"related": map[string]any{
			"workflow_job_template": jt("/api/v2/workflow_job_templates/%d/", awxID(wr.WorkflowName)),
		},
		// The native id, so an operator who found the execution through an AWX client can descend
		// to /api/v1/workflow-runs/{id} — where the per-Step Runs, Gates and events actually live
		// (§1.8). AWX has no field for this, so it gets a namespaced one rather than a hijacked one.
		"summary_fields": map[string]any{
			"stratt": map[string]any{
				"workflow_run": wr.ID,
				"workflow":     wr.WorkflowName,
				"triggered_by": wr.TriggeredBy,
			},
		},
	}
	return out
}

// listWorkflowJobs: GET /api/v2/workflow_jobs/.
func (f *Facade) listWorkflowJobs(w http.ResponseWriter, r *http.Request) {
	wrs, err := f.cfg.Store.ListWorkflowRuns(r.Context(), 0)
	if err != nil {
		awxErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]named, 0, len(wrs))
	for _, wr := range wrs {
		items = append(items, named{id: awxID(wr.ID), name: wr.WorkflowName, obj: workflowRunToJob(wr)})
	}
	writeJSON(w, http.StatusOK, paginate(r, items))
}

// getWorkflowJob: GET /api/v2/workflow_jobs/{id}/ — an indexed lookup (migration 00048), never a
// scan over the recent list, which would 404 a live execution just past the list's horizon.
func (f *Facade) getWorkflowJob(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		awxErr(w, http.StatusNotFound, "Not found.")
		return
	}
	wr, err := f.cfg.Store.GetWorkflowRunByAWXID(r.Context(), id)
	if err != nil {
		if errors.Is(err, graph.ErrNotFound) {
			awxErr(w, http.StatusNotFound, "Not found.")
		} else {
			awxErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, workflowRunToJob(wr))
}

// NO CANCEL ROUTE, and the reason is a defect this family declined to ship. AWX offers
// workflow_jobs/{id}/cancel/, and adding it here was two lines — but RunDAG has NO cancellation
// handling at all (no ctx.Done, no canceled-status write), and the native API has no workflow-run
// cancel door either. Wiring one only on the compat surface would signal Temporal, tear the
// activities down, and leave graph.workflow_run saying `running` forever: an operator who cancelled
// would be told the execution is still going, which is worse than not offering cancel.
//
// A 404 from the mux says "this façade does not offer it" — absent rather than wrong. Cancelling a
// WorkflowRun is a real gap, and it belongs to the NATIVE door first (a terminal status writer in
// RunDAG), with the façade route following it. Booked, not faked.
