package api

import (
	"net/http"

	"github.com/dstout-devops/stratt/types"
)

// ListSources implements (GET /sources): the registered Sources with home Cell,
// re-home seal state, and this daemon's runtime home-ownership status (ADR-0045).
func (s *Server) ListSources(w http.ResponseWriter, r *http.Request) {
	srcs, err := s.Store.ListSources(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	var status map[string]string
	if s.SourceStatus != nil {
		status = s.SourceStatus()
	}
	out := make([]Source, 0, len(srcs))
	for _, src := range srcs {
		out = append(out, toAPISource(src, status))
	}
	writeJSON(w, http.StatusOK, out)
}

// GetSource implements (GET /sources/{name}): returns the Source ONLY if this Cell
// authoritatively homes it — the Connector home probe (ADR-0045). A Source homed
// on a peer Cell (a standby residue) or absent is 404. A peer's fleet-home
// resolver reaches this as an HMAC-signed fan-out GET; the 200/404 answer plus the
// returned cell/rehomingTo tells it whether — and how — the peer homes the Source.
func (s *Server) GetSource(w http.ResponseWriter, r *http.Request, name SourceName) {
	src, err := s.Store.GetSource(r.Context(), name)
	if err != nil {
		s.fail(w, err) // 404 when not registered here
		return
	}
	if !s.homesSource(src) {
		writeErr(w, http.StatusNotFound, "source is not homed on this cell")
		return
	}
	var status map[string]string
	if s.SourceStatus != nil {
		status = s.SourceStatus()
	}
	writeJSON(w, http.StatusOK, toAPISource(src, status))
}

// homesSource reports whether THIS daemon's Cell authoritatively homes the Source:
// its home Cell equals this daemon's, or (on a 'local' daemon) it is an
// unclaimed/'local' Source. A cached row naming a peer as home is NOT homed here.
func (s *Server) homesSource(src types.Source) bool {
	lc := s.localCell()
	if src.Cell == lc {
		return true
	}
	return lc == types.LocalCell && (src.Cell == "" || src.Cell == types.LocalCell)
}

// toAPISource maps a stored Source to the wire model, folding in this daemon's
// runtime home-ownership status when known.
func toAPISource(src types.Source, status map[string]string) Source {
	out := Source{Kind: src.Kind, Name: src.Name, Endpoint: src.Endpoint}
	if src.ID != "" {
		out.Id = &src.ID
	}
	if src.CredentialRef != "" {
		out.CredentialRef = &src.CredentialRef
	}
	if src.Cell != "" {
		out.Cell = &src.Cell
	}
	if src.RehomingTo != "" {
		out.RehomingTo = &src.RehomingTo
	}
	if st, ok := status[src.Name]; ok && st != "" {
		out.Status = &st
	}
	return out
}
