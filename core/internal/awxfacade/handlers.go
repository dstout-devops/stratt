package awxfacade

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

// pathID parses the {id} path value as an AWX integer id.
func pathID(r *http.Request) (int64, bool) {
	n, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// listJobTemplates: GET /api/v2/job_templates/ — single-Step Workflows only.
func (f *Facade) listJobTemplates(w http.ResponseWriter, r *http.Request) {
	wfs, err := f.cfg.Store.ListWorkflows(r.Context())
	if err != nil {
		awxErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]named, 0, len(wfs))
	for _, wf := range wfs {
		step, ok := singleActuationStep(wf)
		if !ok {
			continue // multi-Step/gated Workflows are workflow_job_templates (fast-follow)
		}
		items = append(items, named{id: awxID(wf.Name), name: wf.Name, obj: workflowToJobTemplate(wf, step)})
	}
	writeJSON(w, http.StatusOK, paginate(r, items))
}

// getJobTemplate: GET /api/v2/job_templates/{id}/.
func (f *Facade) getJobTemplate(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, workflowToJobTemplate(wf, step))
}

// resolveJobTemplate reverse-matches an AWX job_template id to a single-Step
// Workflow (Workflows are few and name-keyed — a scan is fine).
func (f *Facade) resolveJobTemplate(r *http.Request, id int64) (types.Workflow, types.Step, bool) {
	wfs, err := f.cfg.Store.ListWorkflows(r.Context())
	if err != nil {
		return types.Workflow{}, types.Step{}, false
	}
	for _, wf := range wfs {
		if awxID(wf.Name) != id {
			continue
		}
		if step, ok := singleActuationStep(wf); ok {
			return wf, step, true
		}
	}
	return types.Workflow{}, types.Step{}, false
}

// listInventories: GET /api/v2/inventories/ (from Views).
func (f *Facade) listInventories(w http.ResponseWriter, r *http.Request) {
	views, err := f.cfg.Store.ListViews(r.Context())
	if err != nil {
		awxErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]named, 0, len(views))
	for _, v := range views {
		total, _ := f.cfg.Store.CountSelector(r.Context(), v.Selector)
		items = append(items, named{id: awxID(v.Name), name: v.Name, obj: viewToInventory(v, int(total))})
	}
	writeJSON(w, http.StatusOK, paginate(r, items))
}

// getInventory: GET /api/v2/inventories/{id}/.
func (f *Facade) getInventory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		awxErr(w, http.StatusNotFound, "Not found.")
		return
	}
	views, err := f.cfg.Store.ListViews(r.Context())
	if err != nil {
		awxErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, v := range views {
		if awxID(v.Name) == id {
			total, _ := f.cfg.Store.CountSelector(r.Context(), v.Selector)
			writeJSON(w, http.StatusOK, viewToInventory(v, int(total)))
			return
		}
	}
	awxErr(w, http.StatusNotFound, "Not found.")
}

// listJobs: GET /api/v2/jobs/ (from recent Runs).
func (f *Facade) listJobs(w http.ResponseWriter, r *http.Request) {
	runs, err := f.cfg.Store.ListRuns(r.Context(), 0)
	if err != nil {
		awxErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]named, 0, len(runs))
	for _, run := range runs {
		items = append(items, named{id: awxID(run.ID), name: run.ID, obj: runToJob(run)})
	}
	writeJSON(w, http.StatusOK, paginate(r, items))
}

// getJob: GET /api/v2/jobs/{id}/.
func (f *Facade) getJob(w http.ResponseWriter, r *http.Request) {
	run, ok := f.runByPathID(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, runToJob(run))
}

// runByPathID resolves the {id} path value to a Run via the AWX-id index,
// writing a 404 and returning ok=false when absent.
func (f *Facade) runByPathID(w http.ResponseWriter, r *http.Request) (types.Run, bool) {
	id, ok := pathID(r)
	if !ok {
		awxErr(w, http.StatusNotFound, "Not found.")
		return types.Run{}, false
	}
	run, err := f.cfg.Store.GetRunByAWXID(r.Context(), id)
	if err != nil {
		if errors.Is(err, graph.ErrNotFound) {
			awxErr(w, http.StatusNotFound, "Not found.")
		} else {
			awxErr(w, http.StatusInternalServerError, err.Error())
		}
		return types.Run{}, false
	}
	return run, true
}
