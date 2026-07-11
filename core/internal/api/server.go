package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"go.temporal.io/sdk/client"

	"github.com/dstout-devops/stratt/core/internal/events"
	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/orchestrate"
	"github.com/dstout-devops/stratt/types"
)

// Server implements the generated ServerInterface over the graph store, the
// event bus, and Temporal — one API for UI, CLI, CI, and agents (§1.6).
type Server struct {
	Store    *graph.Store
	Bus      *events.Bus
	Temporal client.Client
	Log      *slog.Logger
}

// Handler mounts the generated routes under /api/v1.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", http.StripPrefix("/api/v1", Handler(s)))
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, Error{Message: msg})
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, graph.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	s.Log.Error("api error", "error", err)
	writeErr(w, http.StatusInternalServerError, "internal error")
}

// ── mapping helpers (wire ⇄ domain) ─────────────────────────────────────────

func viewToWire(v types.View) View {
	return View{Name: v.Name, Version: v.Version, Selector: selectorToWire(v.Selector)}
}

func selectorToWire(sel types.ViewSelector) ViewSelector {
	out := ViewSelector{}
	if len(sel.Kinds) > 0 {
		out.Kinds = &sel.Kinds
	}
	if len(sel.Labels) > 0 {
		out.Labels = &sel.Labels
	}
	if len(sel.Facets) > 0 {
		preds := make([]FacetPredicate, len(sel.Facets))
		for i, p := range sel.Facets {
			var eq any
			_ = json.Unmarshal(p.Equals, &eq)
			path := p.Path
			preds[i] = FacetPredicate{Namespace: p.Namespace, Path: &path, Equals: eq}
		}
		out.Facets = &preds
	}
	return out
}

func selectorFromWire(in ViewSelector) (types.ViewSelector, error) {
	out := types.ViewSelector{}
	if in.Kinds != nil {
		out.Kinds = *in.Kinds
	}
	if in.Labels != nil {
		out.Labels = *in.Labels
	}
	if in.Facets != nil {
		for _, p := range *in.Facets {
			pred := types.FacetPredicate{Namespace: p.Namespace}
			if p.Path != nil {
				pred.Path = *p.Path
			}
			if p.Equals == nil {
				return out, fmt.Errorf("facet predicate on %s requires equals", p.Namespace)
			}
			raw, err := json.Marshal(p.Equals)
			if err != nil {
				return out, err
			}
			pred.Equals = raw
			out.Facets = append(out.Facets, pred)
		}
	}
	return out, nil
}

func entityToWire(e types.Entity) Entity {
	return Entity{Id: e.ID, Kind: e.Kind, IdentityKeys: e.IdentityKeys, Labels: e.Labels}
}

func runToWire(r types.Run) Run {
	out := Run{
		Id:         r.ID,
		WorkflowId: r.WorkflowID,
		Status:     RunStatus(r.Status),
		StartedAt:  r.StartedAt,
		FinishedAt: r.FinishedAt,
	}
	if r.ViewRef != "" {
		out.ViewRef = &r.ViewRef
	}
	if r.ViewVersion != 0 {
		out.ViewVersion = &r.ViewVersion
	}
	return out
}

// ── handlers ─────────────────────────────────────────────────────────────────

// GetView implements (GET /views/{name}).
func (s *Server) GetView(w http.ResponseWriter, r *http.Request, name ViewName) {
	v, err := s.Store.GetView(r.Context(), name)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, viewToWire(v))
}

// DeclareView implements (PUT /views/{name}).
func (s *Server) DeclareView(w http.ResponseWriter, r *http.Request, name ViewName) {
	var body ViewSelector
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid selector: "+err.Error())
		return
	}
	sel, err := selectorFromWire(body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	v, err := s.Store.DeclareView(r.Context(), name, sel)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, viewToWire(v))
}

// ResolveView implements (GET /views/{name}/entities).
func (s *Server) ResolveView(w http.ResponseWriter, r *http.Request, name ViewName, params ResolveViewParams) {
	limit := 0
	if params.Limit != nil {
		limit = *params.Limit
	}
	v, ents, err := s.Store.ResolveView(r.Context(), name, limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := ViewResolution{View: viewToWire(v), Entities: make([]Entity, len(ents))}
	for i, e := range ents {
		out.Entities[i] = entityToWire(e)
	}
	writeJSON(w, http.StatusOK, out)
}

// GetEntity implements (GET /entities/{id}).
func (s *Server) GetEntity(w http.ResponseWriter, r *http.Request, id string) {
	e, err := s.Store.GetEntity(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	facets, err := s.Store.GetFacets(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	doc := EntityDocument{Entity: entityToWire(e), Facets: make([]Facet, len(facets))}
	for i, f := range facets {
		var val any
		_ = json.Unmarshal(f.Value, &val)
		srcID := f.Provenance.SourceID
		doc.Facets[i] = Facet{
			Namespace: f.Namespace,
			Value:     val,
			Provenance: Provenance{
				WriterKind: ProvenanceWriterKind(f.Provenance.WriterKind),
				WriterRef:  f.Provenance.WriterRef,
				SourceId:   &srcID,
				At:         f.Provenance.At,
			},
		}
	}
	writeJSON(w, http.StatusOK, doc)
}

// StartRun implements (POST /runs): create the Run summary, then start the
// Phase-0 Workflow with the Run id as the Temporal workflow id.
func (s *Server) StartRun(w http.ResponseWriter, r *http.Request) {
	var body StartRun
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	v, err := s.Store.GetView(r.Context(), body.ViewName)
	if err != nil {
		s.fail(w, err)
		return
	}
	run, err := s.Store.CreateRun(r.Context(), "", "view://"+v.Name, v.Version)
	if err != nil {
		s.fail(w, err)
		return
	}
	in := orchestrate.RunInput{RunID: run.ID, ViewName: v.Name}
	if body.Actuator != nil {
		if !body.Actuator.Valid() {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("unknown actuator %q", *body.Actuator))
			return
		}
		in.Actuator = string(*body.Actuator)
	}
	if body.Params != nil {
		raw, err := json.Marshal(*body.Params)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid params: "+err.Error())
			return
		}
		in.Params = raw
	}
	wfID := "run-" + run.ID
	_, err = s.Temporal.ExecuteWorkflow(r.Context(), client.StartWorkflowOptions{
		ID:        wfID,
		TaskQueue: orchestrate.TaskQueue,
	}, orchestrate.RunAgainstView, in)
	if err != nil {
		_ = s.Store.SetRunStatus(r.Context(), run.ID, types.RunFailed, map[string]any{"error": "workflow start failed"})
		s.fail(w, fmt.Errorf("start workflow: %w", err))
		return
	}
	run.WorkflowID = wfID
	if err := s.Store.SetRunWorkflowID(r.Context(), run.ID, wfID); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, runToWire(run))
}

// GetRun implements (GET /runs/{id}).
func (s *Server) GetRun(w http.ResponseWriter, r *http.Request, id RunID) {
	run, err := s.Store.GetRun(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runToWire(run))
}

// TailRunEvents implements (GET /runs/{id}/events): SSE replay + follow of
// the full event stream — never truncated (ADR-0003 L1/L2).
func (s *Server) TailRunEvents(w http.ResponseWriter, r *http.Request, id RunID) {
	if _, err := s.Store.GetRun(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	err := s.Bus.Tail(r.Context(), id, func(ev types.RunEvent) error {
		payload, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Kind, payload); err != nil {
			return err
		}
		flusher.Flush()
		if ev.Kind == "stream-end" {
			return errStreamEnd
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStreamEnd) && r.Context().Err() == nil {
		s.Log.Error("sse tail", "run", id, "error", err)
	}
}

var errStreamEnd = errors.New("stream end")
