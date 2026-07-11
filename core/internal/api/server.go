package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"go.temporal.io/sdk/client"

	"github.com/dstout-devops/stratt/core/internal/authz"
	"github.com/dstout-devops/stratt/core/internal/desiredstate"
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
	Authz    authz.Authorizer
	Log      *slog.Logger
	// DevPrincipalHeader enables the Phase-1 X-Stratt-Principal resolver —
	// dev harness only, replaced by OIDC (ADR-0009). Startup logs loudly.
	DevPrincipalHeader bool
}

// Handler mounts the generated routes under /api/v1, behind the Principal
// resolver — one identity seam for every surface (§1.6, ADR-0009).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", http.StripPrefix("/api/v1", s.principalMiddleware(Handler(s))))
	return mux
}

// principalMiddleware resolves the request Principal. Bearer tokens are
// reserved for OIDC (next slice); the dev header requires explicit opt-in.
// Anonymous requests proceed unresolved — endpoints that require a grant
// deny them (default deny lives at the check, not here).
func (s *Server) principalMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.DevPrincipalHeader {
			if id := r.Header.Get("X-Stratt-Principal"); id != "" {
				kind := r.Header.Get("X-Stratt-Principal-Kind")
				if kind == "" {
					kind = authz.KindHuman
				}
				r = r.WithContext(authz.WithPrincipal(r.Context(), id, kind))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requireGrant checks the request Principal for a relation on an object,
// writing the 403 itself when denied. use-without-read and friends all
// funnel through this one seam.
func (s *Server) requireGrant(w http.ResponseWriter, r *http.Request, relation, object string) bool {
	id, _, ok := authz.PrincipalFrom(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "no principal (authentication required)")
		return false
	}
	allowed, err := s.Authz.Check(r.Context(), id, relation, object)
	if err != nil {
		s.fail(w, err)
		return false
	}
	if !allowed {
		writeErr(w, http.StatusForbidden, fmt.Sprintf("principal %s lacks %s on %s", id, relation, object))
		return false
	}
	return true
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
	out := View{Name: v.Name, Version: v.Version, Selector: selectorToWire(v.Selector)}
	if v.DeclaredBy != "" {
		db := ViewDeclaredBy(v.DeclaredBy)
		out.DeclaredBy = &db
	}
	return out
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
	if errors.Is(err, graph.ErrDeclaredByCaC) {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, viewToWire(v))
}

// ── CredentialRefs (§2.5, ADR-0009) ─────────────────────────────────────────
// Pointer metadata only, always: no handler here (or anywhere) can return
// secret material — no such code path exists.

func credentialRefToWire(ref types.CredentialRef) CredentialRef {
	var locator map[string]any
	_ = json.Unmarshal(ref.Locator, &locator)
	out := CredentialRef{
		Name:      ref.Name,
		OwnerTeam: ref.OwnerTeam,
		Backend:   CredentialRefBackend(ref.Backend),
		Locator:   locator,
		Injection: make([]CredentialInjection, len(ref.Injection)),
	}
	for i, inj := range ref.Injection {
		out.Injection[i] = CredentialInjection{Key: inj.Key, As: CredentialInjectionAs(inj.As), Name: inj.Name}
	}
	if ref.DeclaredBy != "" {
		db := CredentialRefDeclaredBy(ref.DeclaredBy)
		out.DeclaredBy = &db
	}
	return out
}

func credentialRefFromWire(in CredentialRef) (types.CredentialRef, error) {
	if in.Name == "" || in.OwnerTeam == "" {
		return types.CredentialRef{}, fmt.Errorf("name and ownerTeam are required")
	}
	if !in.Backend.Valid() {
		return types.CredentialRef{}, fmt.Errorf("unknown backend %q", in.Backend)
	}
	locator, err := json.Marshal(in.Locator)
	if err != nil {
		return types.CredentialRef{}, err
	}
	if len(in.Injection) == 0 {
		return types.CredentialRef{}, fmt.Errorf("injection policy is required")
	}
	out := types.CredentialRef{
		Name: in.Name, OwnerTeam: in.OwnerTeam, Backend: string(in.Backend),
		Locator: locator, Injection: make([]types.CredentialInjection, len(in.Injection)),
	}
	for i, inj := range in.Injection {
		if inj.Key == "" || inj.Name == "" || !inj.As.Valid() {
			return types.CredentialRef{}, fmt.Errorf("injection %d: key, name, and as (env|file) are required", i)
		}
		out.Injection[i] = types.CredentialInjection{Key: inj.Key, As: string(inj.As), Name: inj.Name}
	}
	return out, nil
}

// GetCredentialRef implements (GET /credential-refs/{name}) — reader grant.
func (s *Server) GetCredentialRef(w http.ResponseWriter, r *http.Request, name string) {
	if !s.requireGrant(w, r, authz.RelationReader, "credential_ref:"+name) {
		return
	}
	ref, err := s.Store.GetCredentialRef(r.Context(), name)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, credentialRefToWire(ref))
}

// ListCredentialRefs implements (GET /credential-refs): only pointers the
// Principal may read are returned.
func (s *Server) ListCredentialRefs(w http.ResponseWriter, r *http.Request) {
	id, _, ok := authz.PrincipalFrom(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "no principal (authentication required)")
		return
	}
	out := []CredentialRef{}
	for _, declaredBy := range []string{graph.DeclaredByAPI, graph.DeclaredByCaC} {
		refs, err := s.Store.ListCredentialRefsDeclaredBy(r.Context(), declaredBy)
		if err != nil {
			s.fail(w, err)
			return
		}
		for _, ref := range refs {
			allowed, err := s.Authz.Check(r.Context(), id, authz.RelationReader, "credential_ref:"+ref.Name)
			if err != nil {
				s.fail(w, err)
				return
			}
			if allowed {
				out = append(out, credentialRefToWire(ref))
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// DeclareCredentialRef implements (PUT /credential-refs/{name}) — admin
// grant on the ref (owner-team admins hold it via the model).
func (s *Server) DeclareCredentialRef(w http.ResponseWriter, r *http.Request, name string) {
	var body CredentialRef
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid credential ref: "+err.Error())
		return
	}
	body.Name = name
	ref, err := credentialRefFromWire(body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.requireGrant(w, r, authz.RelationAdmin, "credential_ref:"+name) {
		return
	}
	declared, err := s.Store.DeclareCredentialRefAs(r.Context(), ref, graph.DeclaredByAPI)
	if errors.Is(err, graph.ErrDeclaredByCaC) {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, credentialRefToWire(declared))
}

// ── desired state (§1.2: drift is the diff) ─────────────────────────────────

func declarationsFromWire(in DesiredState) (desiredstate.Declarations, error) {
	var out desiredstate.Declarations
	out.Views = make([]desiredstate.Declaration, len(in.Views))
	for i, d := range in.Views {
		if d.Name == "" {
			return out, fmt.Errorf("declaration %d: name is required", i)
		}
		sel, err := selectorFromWire(d.Selector)
		if err != nil {
			return out, fmt.Errorf("declaration %s: %w", d.Name, err)
		}
		out.Views[i] = desiredstate.Declaration{Name: d.Name, Selector: sel}
	}
	if in.CredentialRefs != nil {
		for _, c := range *in.CredentialRefs {
			ref, err := credentialRefFromWire(c)
			if err != nil {
				return out, fmt.Errorf("credential ref %s: %w", c.Name, err)
			}
			out.CredentialRefs = append(out.CredentialRefs, ref)
		}
	}
	return out, nil
}

func planToWire(p desiredstate.Plan) Plan {
	out := Plan{Entries: make([]PlanEntry, len(p.Entries))}
	for i, e := range p.Entries {
		kind := PlanEntryKind(e.Kind)
		w := PlanEntry{Kind: &kind, Name: e.Name, Action: PlanEntryAction(e.Action), MemberCount: e.MemberCount}
		if e.OldSelector != nil {
			s := selectorToWire(*e.OldSelector)
			w.OldSelector = &s
		}
		if e.NewSelector != nil {
			s := selectorToWire(*e.NewSelector)
			w.NewSelector = &s
		}
		if e.Error != "" {
			msg := e.Error
			w.Error = &msg
		}
		out.Entries[i] = w
	}
	return out
}

func (s *Server) desiredStateBody(w http.ResponseWriter, r *http.Request) (desiredstate.Declarations, bool) {
	var body DesiredState
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid desired state: "+err.Error())
		return desiredstate.Declarations{}, false
	}
	decls, err := declarationsFromWire(body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return desiredstate.Declarations{}, false
	}
	return decls, true
}

// DesiredStatePlan implements (POST /desired-state/plan).
func (s *Server) DesiredStatePlan(w http.ResponseWriter, r *http.Request) {
	decls, ok := s.desiredStateBody(w, r)
	if !ok {
		return
	}
	plan, err := desiredstate.ComputePlan(r.Context(), s.Store, decls)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, planToWire(plan))
}

// DesiredStateApply implements (POST /desired-state/apply).
func (s *Server) DesiredStateApply(w http.ResponseWriter, r *http.Request) {
	decls, ok := s.desiredStateBody(w, r)
	if !ok {
		return
	}
	plan, err := desiredstate.Apply(r.Context(), s.Store, decls)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, planToWire(plan))
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
	// The launching Principal rides the Run for the dispatch-time `use`
	// check and the audit trail (§1.8). Anonymous launches carry none and
	// fail credential resolution if refs are requested.
	if id, _, ok := authz.PrincipalFrom(r.Context()); ok {
		in.Principal = id
	}
	if body.CredentialRefs != nil {
		in.CredentialRefs = *body.CredentialRefs
	}
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
	if body.Slices != nil {
		if *body.Slices < 1 {
			writeErr(w, http.StatusBadRequest, "slices must be >= 1")
			return
		}
		in.Slices = int(*body.Slices)
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

	// Independent floor (§1.8, charter-guardian): the stream-end marker is
	// published by FinishRun and could be lost if the bus fails at exactly
	// that moment. A tail must never hang on a Run that is durably terminal
	// in Postgres — poll the summary and close after a grace period that
	// lets a late marker win the race.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run, err := s.Store.GetRun(ctx, id)
				if err != nil || run.FinishedAt == nil {
					continue
				}
				select { // grace: the marker normally arrives first
				case <-time.After(5 * time.Second):
					cancel()
				case <-ctx.Done():
				}
				return
			}
		}
	}()

	err := s.Bus.Tail(ctx, id, func(ev types.RunEvent) error {
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
	if err != nil && !errors.Is(err, errStreamEnd) && r.Context().Err() == nil && ctx.Err() == nil {
		s.Log.Error("sse tail", "run", id, "error", err)
	}
}

var errStreamEnd = errors.New("stream end")
