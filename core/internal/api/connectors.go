package api

import (
	"net/http"

	"github.com/dstout-devops/stratt/types"
)

// Connector + Actuator read surface (ADR-0103): CaC-only declarations projected to the
// graph, surfaced read-only (there is NO API write path — the desired-state engine is the
// sole writer, same posture as Triggers). The detail endpoints attach the runtime registry
// status (D6, §1.8): a declared integration that is not currently running shows WHY. The
// declaration JSON is written straight from types.Connector/types.Actuator (their tags match
// the OpenAPI schema); only the status reuses the generated PluginRuntimeStatus.

func (s *Server) ListConnectors(w http.ResponseWriter, r *http.Request) {
	cs, err := s.Store.ListConnectors(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if cs == nil {
		cs = []types.Connector{}
	}
	writeJSON(w, http.StatusOK, cs)
}

func (s *Server) GetConnector(w http.ResponseWriter, r *http.Request, name string) {
	c, err := s.Store.GetConnector(r.Context(), name)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Connector types.Connector      `json:"connector"`
		Status    *PluginRuntimeStatus `json:"status,omitempty"`
	}{Connector: c, Status: s.pluginStatus("connector/" + name)})
}

func (s *Server) ListActuators(w http.ResponseWriter, r *http.Request) {
	as, err := s.Store.ListActuators(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if as == nil {
		as = []types.Actuator{}
	}
	writeJSON(w, http.StatusOK, as)
}

func (s *Server) GetActuator(w http.ResponseWriter, r *http.Request, name string) {
	a, err := s.Store.GetActuator(r.Context(), name)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Actuator types.Actuator       `json:"actuator"`
		Status   *PluginRuntimeStatus `json:"status,omitempty"`
	}{Actuator: a, Status: s.pluginStatus("actuator/" + name)})
}

// pluginStatus looks up one declaration's runtime registry status (key "<kind>/<name>"),
// nil when no registry status provider is wired or the entry is absent.
func (s *Server) pluginStatus(key string) *PluginRuntimeStatus {
	if s.PluginStatus == nil {
		return nil
	}
	if st, ok := s.PluginStatus()[key]; ok {
		return &st
	}
	return nil
}
