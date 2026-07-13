package awxfacade

import (
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

// viewNameFromRef strips the "view://" scheme from a Run's ViewRef → the bare
// name used as the authz object (view:<name>).
func viewNameFromRef(ref string) string {
	return strings.TrimPrefix(ref, "view://")
}
