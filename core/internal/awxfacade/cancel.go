package awxfacade

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/dstout-devops/stratt/core/internal/orchestrate"
	"github.com/dstout-devops/stratt/types"
)

// canCancel: GET /api/v2/jobs/{id}/cancel/ → {"can_cancel": bool}, from the
// Run's state.
func (f *Facade) canCancel(w http.ResponseWriter, r *http.Request) {
	run, ok := f.runByPathID(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"can_cancel": cancelable(run.Status)})
}

// cancel: POST /api/v2/jobs/{id}/cancel/ → 202. Wraps the native cancel; the
// Run's Temporal workflow owns the canceled transition and Job cleanup
// (ADR-0026). Authorization matches the native cancel exactly — authenticated
// (the authed middleware), object-gating deferred with Run/View-scoped execution
// authz — so the façade is not a weaker path than /api/v1 (§1.6).
func (f *Facade) cancel(w http.ResponseWriter, r *http.Request) {
	run, ok := f.runByPathID(w, r)
	if !ok {
		return
	}
	id, _, _ := principal(r)
	if !f.requireRunner(r.Context(), w, id, viewNameFromRef(run.ViewRef)) {
		return
	}
	if !cancelable(run.Status) {
		// Already terminal — AWX treats a re-cancel as a 202 no-op.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if err := orchestrate.CancelRun(r.Context(), f.cfg.Temporal, run.ID); err != nil {
		awxErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func cancelable(s types.RunStatus) bool {
	return s == types.RunPending || s == types.RunRunning
}

// canCancelWorkflowJob: GET /api/v2/workflow_jobs/{id}/cancel/ → {"can_cancel": bool}.
//
// `can_cancel` stops being a field with no mechanism behind it (ADR-0157): until RunDAG had a
// terminal-status writer, this family shipped NO cancel route at all and said so in a comment,
// because signalling Temporal would have left graph.workflow_run reading `running` forever.
func (f *Facade) canCancelWorkflowJob(w http.ResponseWriter, r *http.Request) {
	wr, ok := f.workflowRunByPathID(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"can_cancel": cancelable(wr.Status)})
}

// cancelWorkflowJob: POST /api/v2/workflow_jobs/{id}/cancel/ → 202. The route 1d7ffc0 declined and
// ADR-0157 unblocks. Wraps the native cancel: RunDAG owns the `canceled` transition, its pending
// Gates, and the per-Step summary; children are reaped by ParentClosePolicy.
//
// AUTHORIZATION MATCHES THE NATIVE DOOR — the runner grant on EVERY actuation Step's View, not just
// the first, which is the same shape this family's launch already uses. A compat surface that
// authorized cancel more loosely than /api/v1 would be a weaker path to the same capability, and
// §1.6 says there is one model. The inherited View (a Finding remediation, whose Steps name none of
// their own) comes from the row, which is why ADR-0157 D3 needed it persisted.
func (f *Facade) cancelWorkflowJob(w http.ResponseWriter, r *http.Request) {
	wr, ok := f.workflowRunByPathID(w, r)
	if !ok {
		return
	}
	wf, err := f.cfg.Store.GetWorkflow(r.Context(), wr.WorkflowName)
	if err != nil {
		awxErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	id, _, _ := principal(r)
	for _, st := range wf.Steps {
		if !st.IsActuation() {
			continue
		}
		view := st.ViewName
		if view == "" {
			view = wr.ViewName
		}
		if view == "" {
			// An actuation Step with no View of its own and nothing recorded to inherit. Refuse
			// rather than skip the check: an omitted field must not become a bypassed gate.
			awxErr(w, http.StatusBadRequest, fmt.Sprintf(
				"workflow %s step %q converges a View but names none, and this execution recorded "+
					"none to inherit — its cancel cannot be authorized", wf.Name, st.Name))
			return
		}
		if !f.requireRunner(r.Context(), w, id, view) {
			return
		}
	}
	if !cancelable(wr.Status) {
		w.WriteHeader(http.StatusAccepted) // already terminal — AWX treats a re-cancel as a no-op
		return
	}
	if err := orchestrate.CancelWorkflowRun(r.Context(), f.cfg.Temporal, wr.ID, wr.TemporalID); err != nil {
		awxErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// viewNameFromRef strips the "view://" scheme from a Run's ViewRef → the bare
// name used as the authz object (view:<name>).
func viewNameFromRef(ref string) string {
	return strings.TrimPrefix(ref, "view://")
}
