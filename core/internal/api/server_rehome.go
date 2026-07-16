package api

import (
	"encoding/json"
	"net/http"

	"go.temporal.io/sdk/client"

	"github.com/dstout-devops/stratt/core/internal/authz"
	"github.com/dstout-devops/stratt/core/internal/orchestrate"
	"github.com/dstout-devops/stratt/types"
)

// RehomeSource implements (POST /sources/{name}/rehome): start a fenced
// cross-Cell Source re-home (ADR-0044 slice 7). A Source is homed on exactly one
// Cell (its registering daemon's Cell). If homed here, this is the Source's home
// Cell — check the `rehome` grant on the destination and start the durable
// RehomeSourceWorkflow (202, async). If homed on a peer, forward the request to
// that Cell (§1.8: the move must run where the Source lives; an unreachable home
// is a loud 503, never a silent success). A forwarded request that misses locally
// 404s rather than re-forwarding (loop-safe).
func (s *Server) RehomeSource(w http.ResponseWriter, r *http.Request, name SourceName) {
	var req RehomeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid rehome request body")
		return
	}
	if req.DestCell == "" {
		writeErr(w, http.StatusBadRequest, "destCell is required")
		return
	}

	src, err := s.Store.GetSource(r.Context(), name)
	if err == nil && (src.Cell == "" || src.Cell == s.localCell()) {
		// Homed here: this Cell owns the move. Authorize against the DESTINATION
		// Cell (deny-by-default) and launch the workflow, which seals locally,
		// forwards the adopt to the destination, then tombstones the old Entities.
		if !s.requireGrant(w, r, authz.RelationRehome, authz.CellObject(req.DestCell)) {
			return
		}
		if s.Temporal == nil {
			writeErr(w, http.StatusServiceUnavailable, "orchestration unavailable")
			return
		}
		pid, _, _ := authz.PrincipalFrom(r.Context())
		if _, err := s.Temporal.ExecuteWorkflow(r.Context(), client.StartWorkflowOptions{
			ID: "rehome-" + name, TaskQueue: orchestrate.TaskQueue,
		}, orchestrate.RehomeSourceWorkflow, orchestrate.RehomeInput{
			SourceName: name, DestCell: req.DestCell, Principal: pid,
		}); err != nil {
			s.fail(w, err)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}
	// A forwarded fan-out that does not home the Source here must NOT re-forward
	// (that would storm the mesh) — the home genuinely isn't reachable from this
	// hop, so 404 and let the originator's fan-out find the home Cell.
	if isForwardedChild(r) {
		writeErr(w, http.StatusNotFound, "this Cell does not home the source")
		return
	}
	// Homed on a peer (local miss or a foreign-Cell stamp): forward the re-home to
	// the Cell that owns the Source. The home re-checks the grant; others 404.
	body, _ := json.Marshal(req)
	s.forwardWriteToPeers(w, r, "/sources/"+name+"/rehome", body, err)
}

// AdoptRehomedSource implements (POST /sources/rehome-adopt): the destination
// Cell claims a re-homed Source (ADR-0044 slice 7). Accepted ONLY as a verified
// peer fan-out (the HMAC-gated cellrouter admits the fanout header), and the
// asserted Principal is re-checked for `rehome` on THIS (destination) Cell
// against the global OpenFGA (§1.6). The body carries a CredentialRef NAME only
// (§2.5). Idempotent + epoch-fenced.
func (s *Server) AdoptRehomedSource(w http.ResponseWriter, r *http.Request) {
	if !isForwardedChild(r) {
		writeErr(w, http.StatusForbidden, "rehome-adopt is a peer-internal endpoint (fan-out only)")
		return
	}
	var body RehomeAdopt
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid adopt body")
		return
	}
	// The acting Principal (asserted over the verified fan-out) must hold `rehome`
	// on THIS Cell — the destination re-authorizes the write (§1.6), never trusts
	// the source Cell's check alone.
	if !s.requireGrant(w, r, authz.RelationRehome, authz.CellObject(s.localCell())) {
		return
	}
	src := types.Source{
		Kind:     body.Source.Kind,
		Name:     body.Source.Name,
		Endpoint: body.Source.Endpoint,
	}
	if body.Source.CredentialRef != nil {
		src.CredentialRef = *body.Source.CredentialRef // NAME only (§2.5), resolved against THIS Cell's Secrets
	}
	if err := s.Store.AdoptSource(r.Context(), src, body.Epoch); err != nil {
		s.fail(w, err)
		return
	}
	// Record the adopt on THIS Cell's per-Cell audit chain (§1.8 — the move is
	// logged on both Cells: seal/complete on the source, adopt here). Stamp this
	// Cell so a federated audit view attributes the adopt to the destination, and
	// surface a failed audit write rather than swallow it (§1.8).
	pid, _, _ := authz.PrincipalFrom(r.Context())
	detail, _ := json.Marshal(map[string]any{"phase": types.RehomeAdopted, "epoch": body.Epoch})
	if err := s.Store.RecordAudit(r.Context(), types.AuditEvent{
		PrincipalID: pid, Action: types.AuditRehome, Object: "source:" + src.Name,
		Outcome: types.AuditOK, Detail: detail, Cell: s.localCell(),
	}); err != nil && s.Log != nil {
		s.Log.Error("audit record failed", "action", types.AuditRehome, "source", src.Name, "err", err)
	}
	w.WriteHeader(http.StatusAccepted)
}
