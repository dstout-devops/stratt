package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.temporal.io/sdk/client"

	"github.com/dstout-devops/stratt/core/internal/authz"
	"github.com/dstout-devops/stratt/core/internal/contract"
	"github.com/dstout-devops/stratt/core/internal/desiredstate"
	"github.com/dstout-devops/stratt/core/internal/events"
	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/orchestrate"
	"github.com/dstout-devops/stratt/core/internal/triggers"
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
	// DevPrincipalHeader enables the X-Stratt-Principal resolver — dev
	// harness / no-substrate path only (ADR-0009). Startup logs loudly.
	DevPrincipalHeader bool
	// OIDC, when set, resolves Authorization: Bearer tokens to Principals.
	// Nil leaves Bearer requests anonymous-denied (no silent fallback from
	// a presented-but-unverifiable credential to another resolver).
	OIDC *authz.OIDCResolver
	// UIDir, when set, serves the built UI (ADR-0012) at / with an SPA
	// fallback to index.html. The UI is a pure client of /api/v1 — same API
	// as CLI, CI, and agents (§1.6); go:embed packaging is the Helm slice.
	UIDir string
	// StateBackend, when set, mounts the OpenTofu HTTP state backend at
	// /statebackend/ (ADR-0016) — outside /api/v1: execution pods
	// authenticate with per-workspace HMAC credentials, not Principals.
	StateBackend http.Handler
	// EmitterIngest, when set, mounts POST /emitters/{name} (ADR-0018) —
	// outside /api/v1: alert sources authenticate by emitter token.
	EmitterIngest http.Handler
}

// Handler mounts the generated routes under /api/v1, behind the Principal
// resolver — one identity seam for every surface (§1.6, ADR-0009).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/v1/", http.StripPrefix("/api/v1", s.principalMiddleware(Handler(s))))
	// Probe endpoint (ADR-0013): process-liveness only — no store, no authz,
	// so probes never flap on substrate warm-up or grant state.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	if s.StateBackend != nil {
		mux.Handle("/statebackend/", s.StateBackend)
	}
	if s.EmitterIngest != nil {
		mux.Handle("/emitters/", s.EmitterIngest)
	}
	if s.UIDir != "" {
		mux.Handle("/", spaHandler(s.UIDir))
	}
	return mux
}

// spaHandler serves dir statically, falling back to index.html for paths
// that don't exist on disk (client-side routes are URL-addressable, L10).
func spaHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			fs.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dir, "index.html"))
	})
}

// principalMiddleware resolves the request Principal. Bearer tokens go to
// the OIDC resolver when configured — an invalid or expired token is 401,
// never a silent downgrade to anonymous; the dev header requires explicit
// opt-in. Anonymous requests proceed unresolved — endpoints that require a
// grant deny them (default deny lives at the check, not here).
func (s *Server) principalMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); s.OIDC != nil && strings.HasPrefix(auth, "Bearer ") {
			id, kind, err := s.OIDC.Resolve(r.Context(), strings.TrimPrefix(auth, "Bearer "))
			if err != nil {
				writeErr(w, http.StatusUnauthorized, "invalid bearer token")
				return
			}
			next.ServeHTTP(w, r.WithContext(authz.WithPrincipal(r.Context(), id, kind)))
			return
		}
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
	if r.TriggeredBy != "" {
		out.TriggeredBy = &r.TriggeredBy
	}
	if r.Baseline != "" {
		out.Baseline = &r.Baseline
	}
	if r.WorkflowRunID != "" {
		out.WorkflowRunId = &r.WorkflowRunID
	}
	if r.StepName != "" {
		out.StepName = &r.StepName
	}
	return out
}

func triggerToWire(t types.Trigger) Trigger {
	out := Trigger{Name: t.Name, Kind: TriggerKind(t.Kind)}
	if t.Cron != "" {
		out.Cron = &t.Cron
	}
	if t.ViewName != "" {
		out.ViewName = &t.ViewName
	}
	if t.Emitter != "" {
		out.Emitter = &t.Emitter
	}
	if t.When != "" {
		out.When = &t.When
	}
	if t.CooldownSeconds != 0 {
		n := int64(t.CooldownSeconds)
		out.CooldownSeconds = &n
	}
	if t.WorkflowName != "" {
		out.WorkflowName = &t.WorkflowName
	}
	if t.Paused {
		out.Paused = &t.Paused
	}
	if t.Actuator != "" {
		out.Actuator = &t.Actuator
	}
	if t.Params != nil {
		out.Params = &t.Params
	}
	if t.Slices != 0 {
		s := int64(t.Slices)
		out.Slices = &s
	}
	if len(t.CredentialRefs) > 0 {
		out.CredentialRefs = &t.CredentialRefs
	}
	if t.Principal != "" {
		out.Principal = &t.Principal
	}
	return out
}

func baselineToWire(b types.Baseline) Baseline {
	out := Baseline{Name: b.Name, ViewName: b.ViewName, Cron: b.Cron, Severity: BaselineSeverity(b.Severity)}
	if b.Actuator != "" {
		a := BaselineActuator(b.Actuator)
		out.Actuator = &a
	}
	if b.Params != nil {
		out.Params = &b.Params
	}
	if b.Slices != 0 {
		n := int64(b.Slices)
		out.Slices = &n
	}
	if len(b.CredentialRefs) > 0 {
		out.CredentialRefs = &b.CredentialRefs
	}
	if b.Principal != "" {
		out.Principal = &b.Principal
	}
	if b.Paused {
		out.Paused = &b.Paused
	}
	if b.DampingObservations != 0 {
		n := int64(b.DampingObservations)
		out.DampingObservations = &n
	}
	if b.RemediationWorkflow != "" {
		out.RemediationWorkflow = &b.RemediationWorkflow
	}
	if b.Framework != "" {
		out.Framework = &b.Framework
	}
	return out
}

// baselineFromWire mirrors baselineToWire; same CaC declaration the
// desired-state controller reads from Git (the CLI plan/apply path sends the
// checkout verbatim — Git review stays the authorization).
func baselineFromWire(w Baseline) (types.Baseline, error) {
	b := types.Baseline{Name: w.Name, ViewName: w.ViewName, Cron: w.Cron, Severity: string(w.Severity)}
	if w.Actuator != nil {
		b.Actuator = string(*w.Actuator)
	}
	if w.Params != nil {
		b.Params = *w.Params
	}
	if w.Slices != nil {
		b.Slices = int(*w.Slices)
	}
	if w.CredentialRefs != nil {
		b.CredentialRefs = *w.CredentialRefs
	}
	if w.Principal != nil {
		b.Principal = *w.Principal
	}
	if w.Paused != nil {
		b.Paused = *w.Paused
	}
	if w.DampingObservations != nil {
		b.DampingObservations = int(*w.DampingObservations)
	}
	if w.RemediationWorkflow != nil {
		b.RemediationWorkflow = *w.RemediationWorkflow
	}
	if w.Framework != nil {
		b.Framework = *w.Framework
	}
	if err := desiredstate.ValidateBaseline(b); err != nil {
		return b, err
	}
	return b, nil
}

func findingToWire(f types.Finding) Finding {
	out := Finding{
		Id: f.ID, Baseline: f.Baseline, Target: f.Target,
		Status: FindingStatus(f.Status), Severity: FindingSeverity(f.Severity),
		ConsecutiveDrifted: int64(f.ConsecutiveDrifted),
		FirstObserved:      f.FirstObserved, LastObserved: f.LastObserved,
		OpenedAt: f.OpenedAt, ResolvedAt: f.ResolvedAt,
	}
	if f.EntityID != "" {
		out.EntityId = &f.EntityID
	}
	if f.Framework != "" {
		out.Framework = &f.Framework
	}
	if f.RunID != "" {
		out.RunId = &f.RunID
	}
	if len(f.Diff) > 0 {
		out.Diff = json.RawMessage(f.Diff)
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
	if in.Triggers != nil {
		for _, w := range *in.Triggers {
			t, err := triggerFromWire(w)
			if err != nil {
				return out, fmt.Errorf("trigger %s: %w", w.Name, err)
			}
			out.Triggers = append(out.Triggers, t)
		}
	}
	if in.Workflows != nil {
		for _, w := range *in.Workflows {
			wf, err := workflowFromWire(w)
			if err != nil {
				return out, fmt.Errorf("workflow %s: %w", w.Name, err)
			}
			out.Workflows = append(out.Workflows, wf)
		}
	}
	if in.Emitters != nil {
		for _, e := range *in.Emitters {
			em := types.Emitter{Name: e.Name, Kind: string(e.Kind), TokenHash: strings.ToLower(e.TokenHash)}
			if err := desiredstate.ValidateEmitter(em); err != nil {
				return out, fmt.Errorf("emitter %s: %w", e.Name, err)
			}
			out.Emitters = append(out.Emitters, em)
		}
	}
	if in.Baselines != nil {
		for _, w := range *in.Baselines {
			b, err := baselineFromWire(w)
			if err != nil {
				return out, fmt.Errorf("baseline %s: %w", w.Name, err)
			}
			out.Baselines = append(out.Baselines, b)
		}
	}
	return out, nil
}

// triggerFromWire mirrors triggerToWire; the document is the same CaC
// declaration the desired-state controller reads from Git — the CLI plan/
// apply path sends the checkout verbatim (ADR-0010: Git review stays the
// authorization; there is no other write surface).
func triggerFromWire(w Trigger) (types.Trigger, error) {
	t := types.Trigger{Name: w.Name, Kind: string(w.Kind)}
	if w.Cron != nil {
		t.Cron = *w.Cron
	}
	if w.ViewName != nil {
		t.ViewName = *w.ViewName
	}
	if w.Emitter != nil {
		t.Emitter = *w.Emitter
	}
	if w.When != nil {
		t.When = *w.When
	}
	if w.CooldownSeconds != nil {
		t.CooldownSeconds = int(*w.CooldownSeconds)
	}
	if w.WorkflowName != nil {
		t.WorkflowName = *w.WorkflowName
	}
	if t.Kind == "" {
		t.Kind = types.TriggerSchedule
	}
	if w.Paused != nil {
		t.Paused = *w.Paused
	}
	if w.Actuator != nil {
		t.Actuator = *w.Actuator
	}
	if w.Params != nil {
		t.Params = *w.Params
	}
	if w.Slices != nil {
		t.Slices = int(*w.Slices)
	}
	if w.CredentialRefs != nil {
		t.CredentialRefs = *w.CredentialRefs
	}
	if w.Principal != nil {
		t.Principal = *w.Principal
	}
	if err := desiredstate.ValidateTrigger(t); err != nil {
		return t, err
	}
	return t, nil
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
	// Contract check at the door (§1.5, ADR-0015): a malformed Step fails
	// here with pointer detail — before any Run row exists, not at dispatch.
	if err := validateStepParams(body.Actuator, body.Params); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	v, err := s.Store.GetView(r.Context(), body.ViewName)
	if err != nil {
		s.fail(w, err)
		return
	}
	run, err := s.Store.CreateRun(r.Context(), types.Run{ViewRef: "view://" + v.Name, ViewVersion: v.Version})
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

// ── Triggers (charter §2, ADR-0010) ──────────────────────────────────────────
// CaC-only: declared in the Git desired-state repo (Git review authorizes the
// Principal binding). This surface is read-only by design.

// ListTriggers implements (GET /triggers).
func (s *Server) ListTriggers(w http.ResponseWriter, r *http.Request) {
	ts, err := s.Store.ListTriggers(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]Trigger, 0, len(ts))
	for _, t := range ts {
		out = append(out, triggerToWire(t))
	}
	writeJSON(w, http.StatusOK, out)
}

// GetTrigger implements (GET /triggers/{name}): the declaration plus the
// Temporal Schedule's observed state — the Trigger → Run descent rung (§1.8).
func (s *Server) GetTrigger(w http.ResponseWriter, r *http.Request, name string) {
	t, err := s.Store.GetTrigger(r.Context(), name)
	if err != nil {
		s.fail(w, err)
		return
	}
	detail := TriggerDetail{Trigger: triggerToWire(t)}
	// The schedule is a projection reconciled on a cadence: absent state
	// (not yet created, or Temporal unreachable) degrades to declaration-only
	// rather than failing the read.
	handle := s.Temporal.ScheduleClient().GetHandle(r.Context(), triggers.ScheduleID(name))
	desc, descErr := handle.Describe(r.Context())
	if descErr != nil {
		// Distinguishable in logs (§1.8): "not reconciled yet" and "Temporal
		// unreachable" both degrade the response, but never silently.
		s.Log.Warn("trigger schedule state unavailable; returning declaration only",
			"trigger", name, "error", descErr)
	} else {
		state := TriggerScheduleState{}
		if desc.Schedule.State != nil {
			state.Paused = &desc.Schedule.State.Paused
		}
		if len(desc.Info.NextActionTimes) > 0 {
			state.NextFireTimes = &desc.Info.NextActionTimes
		}
		var recent []struct {
			At         time.Time `json:"at"`
			WorkflowId string    `json:"workflowId"`
		}
		for _, a := range desc.Info.RecentActions {
			if a.StartWorkflowResult == nil {
				continue
			}
			recent = append(recent, struct {
				At         time.Time `json:"at"`
				WorkflowId string    `json:"workflowId"`
			}{At: a.ActualTime, WorkflowId: a.StartWorkflowResult.WorkflowID})
		}
		if len(recent) > 0 {
			state.RecentRuns = &recent
		}
		detail.Schedule = &state
	}
	writeJSON(w, http.StatusOK, detail)
}

// ── Workflows + Gates (charter §2, ADR-0011) ─────────────────────────────────
// Workflows are CaC-only in v1 (declared in Git, read-only here); starting an
// execution and deciding a Gate are the runtime surfaces.

func workflowToWire(w types.Workflow) Workflow {
	out := Workflow{Name: w.Name}
	for _, s := range w.Steps {
		out.Steps = append(out.Steps, stepToWire(s))
	}
	return out
}

func stepToWire(s types.Step) Step {
	out := Step{Name: s.Name}
	if len(s.Needs) > 0 {
		out.Needs = &s.Needs
	}
	if s.When != "" {
		w := StepWhen(s.When)
		out.When = &w
	}
	if s.Gate != nil {
		spec := GateSpec{Approvers: approversToWire(s.Gate.Approvers)}
		if s.Gate.TimeoutSeconds != 0 {
			t := int64(s.Gate.TimeoutSeconds)
			spec.TimeoutSeconds = &t
		}
		out.Gate = &spec
	}
	if s.ViewName != "" {
		out.ViewName = &s.ViewName
	}
	if s.Actuator != "" {
		out.Actuator = &s.Actuator
	}
	if s.Params != nil {
		out.Params = &s.Params
	}
	if s.Slices != 0 {
		n := int64(s.Slices)
		out.Slices = &n
	}
	if len(s.CredentialRefs) > 0 {
		out.CredentialRefs = &s.CredentialRefs
	}
	return out
}

func approversToWire(a types.GateApprovers) GateApprovers {
	out := GateApprovers{}
	if len(a.Principals) > 0 {
		out.Principals = &a.Principals
	}
	if len(a.Teams) > 0 {
		out.Teams = &a.Teams
	}
	return out
}

// workflowFromWire mirrors workflowToWire; same CaC document the controller
// reads from Git — the CLI plan/apply path sends the checkout verbatim.
func workflowFromWire(in Workflow) (types.Workflow, error) {
	w := types.Workflow{Name: in.Name}
	for _, s := range in.Steps {
		step := types.Step{Name: s.Name}
		if s.Needs != nil {
			step.Needs = *s.Needs
		}
		if s.When != nil {
			step.When = string(*s.When)
		}
		if s.Gate != nil {
			g := &types.GateSpec{}
			if s.Gate.Approvers.Principals != nil {
				g.Approvers.Principals = *s.Gate.Approvers.Principals
			}
			if s.Gate.Approvers.Teams != nil {
				g.Approvers.Teams = *s.Gate.Approvers.Teams
			}
			if s.Gate.TimeoutSeconds != nil {
				g.TimeoutSeconds = int(*s.Gate.TimeoutSeconds)
			}
			step.Gate = g
		}
		if s.ViewName != nil {
			step.ViewName = *s.ViewName
		}
		if s.Actuator != nil {
			step.Actuator = *s.Actuator
		}
		if s.Params != nil {
			step.Params = *s.Params
		}
		if s.Slices != nil {
			step.Slices = int(*s.Slices)
		}
		if s.CredentialRefs != nil {
			step.CredentialRefs = *s.CredentialRefs
		}
		w.Steps = append(w.Steps, step)
	}
	if err := desiredstate.ValidateWorkflow(w); err != nil {
		return w, err
	}
	return w, nil
}

func workflowRunToWire(wr types.WorkflowRun) WorkflowRun {
	out := WorkflowRun{
		Id: wr.ID, WorkflowName: wr.WorkflowName,
		Status:    WorkflowRunStatus(wr.Status),
		StartedAt: wr.StartedAt, FinishedAt: wr.FinishedAt,
	}
	if wr.TemporalID != "" {
		out.TemporalId = &wr.TemporalID
	}
	if wr.TriggeredBy != "" {
		out.TriggeredBy = &wr.TriggeredBy
	}
	if wr.Principal != "" {
		out.Principal = &wr.Principal
	}
	return out
}

func gateToWire(g types.Gate) Gate {
	out := Gate{
		Id: g.ID, WorkflowRunId: g.WorkflowRunID, Step: g.Step,
		Status: GateStatus(g.Status), Approvers: approversToWire(g.Approvers),
		CreatedAt: g.CreatedAt, DecidedAt: g.DecidedAt,
	}
	if g.DecidedBy != "" {
		out.DecidedBy = &g.DecidedBy
	}
	if g.Note != "" {
		out.Note = &g.Note
	}
	return out
}

// ListWorkflows implements (GET /workflows).
func (s *Server) ListWorkflows(w http.ResponseWriter, r *http.Request) {
	ws, err := s.Store.ListWorkflows(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]Workflow, 0, len(ws))
	for _, wf := range ws {
		out = append(out, workflowToWire(wf))
	}
	writeJSON(w, http.StatusOK, out)
}

// GetWorkflow implements (GET /workflows/{name}).
func (s *Server) GetWorkflow(w http.ResponseWriter, r *http.Request, name string) {
	wf, err := s.Store.GetWorkflow(r.Context(), name)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workflowToWire(wf))
}

// StartWorkflowRun implements (POST /workflows/{name}/runs): create the
// execution record, then start the RunDAG Temporal workflow. The launching
// Principal rides every Step's credential use check (§2.5).
func (s *Server) StartWorkflowRun(w http.ResponseWriter, r *http.Request, name string) {
	if _, err := s.Store.GetWorkflow(r.Context(), name); err != nil {
		s.fail(w, err)
		return
	}
	principal := ""
	if id, _, ok := authz.PrincipalFrom(r.Context()); ok {
		principal = id
	}
	wr, err := s.Store.CreateWorkflowRun(r.Context(), name, "", principal, "")
	if err != nil {
		s.fail(w, err)
		return
	}
	temporalID := "wfrun-" + wr.ID
	_, err = s.Temporal.ExecuteWorkflow(r.Context(), client.StartWorkflowOptions{
		ID:        temporalID,
		TaskQueue: orchestrate.TaskQueue,
	}, orchestrate.RunDAG, orchestrate.DAGInput{
		WorkflowRunID: wr.ID, WorkflowName: name, Principal: principal,
	})
	if err != nil {
		_ = s.Store.SetWorkflowRunStatus(r.Context(), wr.ID, types.RunFailed, map[string]any{"error": "workflow start failed"})
		s.fail(w, fmt.Errorf("start workflow run: %w", err))
		return
	}
	wr.TemporalID = temporalID
	if err := s.Store.SetWorkflowRunTemporalID(r.Context(), wr.ID, temporalID); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, workflowRunToWire(wr))
}

// GetWorkflowRun implements (GET /workflow-runs/{id}): the Workflow → Run
// descent rung (§1.8) — every Step links its Run or Gate.
func (s *Server) GetWorkflowRun(w http.ResponseWriter, r *http.Request, id string) {
	wr, summary, err := s.Store.GetWorkflowRun(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	wf, err := s.Store.GetWorkflow(r.Context(), wr.WorkflowName)
	if err != nil && !errors.Is(err, graph.ErrNotFound) {
		s.fail(w, err)
		return
	}
	runs, err := s.Store.ListRunsForWorkflowRun(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	gates, err := s.Store.ListGatesForWorkflowRun(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	runByStep := map[string]string{}
	for _, run := range runs {
		runByStep[run.StepName] = run.ID
	}
	gateByStep := map[string]string{}
	for _, g := range gates {
		gateByStep[g.Step] = g.ID
	}
	// Terminal per-Step outcomes, when recorded.
	statusByStep := map[string]string{}
	if steps, ok := summary["steps"].(map[string]any); ok {
		for step, st := range steps {
			if v, ok := st.(string); ok {
				statusByStep[step] = v
			}
		}
	}

	detail := WorkflowRunDetail{WorkflowRun: workflowRunToWire(wr)}
	for _, step := range wf.Steps {
		entry := struct {
			GateId *string `json:"gateId,omitempty"`
			Name   string  `json:"name"`
			RunId  *string `json:"runId,omitempty"`
			Status *string `json:"status,omitempty"`
		}{Name: step.Name}
		if rid, ok := runByStep[step.Name]; ok {
			entry.RunId = &rid
		}
		if gid, ok := gateByStep[step.Name]; ok {
			entry.GateId = &gid
		}
		if st, ok := statusByStep[step.Name]; ok {
			entry.Status = &st
		}
		detail.Steps = append(detail.Steps, entry)
	}
	writeJSON(w, http.StatusOK, detail)
}

// ListGates implements (GET /gates): the approval inbox.
func (s *Server) ListGates(w http.ResponseWriter, r *http.Request, params ListGatesParams) {
	status := ""
	if params.Status != nil {
		status = string(*params.Status)
	}
	gs, err := s.Store.ListGates(r.Context(), status)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]Gate, 0, len(gs))
	for _, g := range gs {
		out = append(out, gateToWire(g))
	}
	writeJSON(w, http.StatusOK, out)
}

// DecideGate implements (POST /gates/{id}/decision): authorize the caller
// against the Gate's pinned approver policy, then deliver the decision to
// the waiting Workflow as a signal — the Workflow records it (§1.8: the
// transition lives in its history).
func (s *Server) DecideGate(w http.ResponseWriter, r *http.Request, id string) {
	var body GateDecision
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid decision: "+err.Error())
		return
	}
	g, err := s.Store.GetGate(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if g.Status != types.GatePending {
		writeErr(w, http.StatusConflict, fmt.Sprintf("gate %s is already %s", id, g.Status))
		return
	}
	principal, _, ok := authz.PrincipalFrom(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "no principal (authentication required)")
		return
	}
	allowed := false
	for _, p := range g.Approvers.Principals {
		if p == principal {
			allowed = true
			break
		}
	}
	for _, team := range g.Approvers.Teams {
		if allowed {
			break
		}
		member, err := s.Authz.Check(r.Context(), principal, authz.RelationMember, "team:"+team)
		if err != nil {
			s.fail(w, err)
			return
		}
		allowed = member
	}
	if !allowed {
		writeErr(w, http.StatusForbidden, fmt.Sprintf("principal %s is not an approver of gate %s", principal, id))
		return
	}

	wr, _, err := s.Store.GetWorkflowRun(r.Context(), g.WorkflowRunID)
	if err != nil {
		s.fail(w, err)
		return
	}
	note := ""
	if body.Note != nil {
		note = *body.Note
	}
	err = s.Temporal.SignalWorkflow(r.Context(), wr.TemporalID, "", orchestrate.GateSignalName(g.Step),
		orchestrate.GateDecision{Approved: body.Approve, Principal: principal, Note: note})
	if err != nil {
		s.fail(w, fmt.Errorf("signal gate decision: %w", err))
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// ── list endpoints (UI slice, ADR-0012) ──────────────────────────────────────

// ListViews implements (GET /views).
func (s *Server) ListViews(w http.ResponseWriter, r *http.Request) {
	vs, err := s.Store.ListViews(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]View, 0, len(vs))
	for _, v := range vs {
		out = append(out, viewToWire(v))
	}
	writeJSON(w, http.StatusOK, out)
}

// ListRuns implements (GET /runs).
func (s *Server) ListRuns(w http.ResponseWriter, r *http.Request, params ListRunsParams) {
	limit := 0
	if params.Limit != nil {
		limit = int(*params.Limit)
	}
	rs, err := s.Store.ListRuns(r.Context(), limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]Run, 0, len(rs))
	for _, run := range rs {
		out = append(out, runToWire(run))
	}
	writeJSON(w, http.StatusOK, out)
}

// ListWorkflowRuns implements (GET /workflow-runs).
func (s *Server) ListWorkflowRuns(w http.ResponseWriter, r *http.Request, params ListWorkflowRunsParams) {
	limit := 0
	if params.Limit != nil {
		limit = int(*params.Limit)
	}
	wrs, err := s.Store.ListWorkflowRuns(r.Context(), limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]WorkflowRun, 0, len(wrs))
	for _, wr := range wrs {
		out = append(out, workflowRunToWire(wr))
	}
	writeJSON(w, http.StatusOK, out)
}

// validateStepParams checks wire Step params against the Actuator's input
// Contract (§1.5, ADR-0015). nil actuator means the ansible default.
func validateStepParams(actuator *StartRunActuator, params *map[string]interface{}) error {
	name := "ansible"
	if actuator != nil && *actuator != "" {
		name = string(*actuator)
	}
	raw := json.RawMessage(`{}`)
	if params != nil {
		b, err := json.Marshal(*params)
		if err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
		raw = b
	}
	return contract.ValidateActuatorParams(name, raw)
}

// ListContracts implements (GET /contracts): the pin registry — shipped
// documents (startup-verified against their pins) AND tool-derived ones
// (ADR-0017), with their full version history.
func (s *Server) ListContracts(w http.ResponseWriter, r *http.Request) {
	all, err := s.Store.ListContracts(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]Contract, 0, len(all))
	for _, c := range all {
		var doc map[string]interface{}
		if err := json.Unmarshal(c.Schema, &doc); err != nil {
			s.fail(w, err)
			return
		}
		out = append(out, Contract{
			Name: c.Name, Version: int64(c.Version), Rung: ContractRung(c.Rung),
			Hash: c.Hash, Schema: doc,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// ListEmitters implements (GET /emitters): declarations only — the token
// hash is the whole secret story (§2.5), so this surface leaks nothing.
func (s *Server) ListEmitters(w http.ResponseWriter, r *http.Request) {
	es, err := s.Store.ListEmitters(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]Emitter, 0, len(es))
	for _, e := range es {
		out = append(out, Emitter{Name: e.Name, Kind: EmitterKind(e.Kind), TokenHash: e.TokenHash})
	}
	writeJSON(w, http.StatusOK, out)
}

// ListBaselines implements (GET /baselines): the declared checkable desired
// state (ADR-0019). Read-only — Baselines are CaC-only.
func (s *Server) ListBaselines(w http.ResponseWriter, r *http.Request) {
	bs, err := s.Store.ListBaselines(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]Baseline, 0, len(bs))
	for _, b := range bs {
		out = append(out, baselineToWire(b))
	}
	writeJSON(w, http.StatusOK, out)
}

// GetBaseline implements (GET /baselines/{name}).
func (s *Server) GetBaseline(w http.ResponseWriter, r *http.Request, name string) {
	b, err := s.Store.GetBaseline(r.Context(), name)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, baselineToWire(b))
}

// ListFindings implements (GET /findings): drift/compliance results across
// every Baseline on one list (§5 Flow 2: one dashboard).
func (s *Server) ListFindings(w http.ResponseWriter, r *http.Request, params ListFindingsParams) {
	baseline, status := "", ""
	if params.Baseline != nil {
		baseline = *params.Baseline
	}
	if params.Status != nil {
		status = string(*params.Status)
	}
	limit := 0
	if params.Limit != nil {
		limit = int(*params.Limit)
	}
	fs, err := s.Store.ListFindings(r.Context(), baseline, status, limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]Finding, 0, len(fs))
	for _, f := range fs {
		out = append(out, findingToWire(f))
	}
	writeJSON(w, http.StatusOK, out)
}

// GetFinding implements (GET /findings/{id}).
func (s *Server) GetFinding(w http.ResponseWriter, r *http.Request, id string) {
	f, err := s.Store.GetFinding(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, findingToWire(f))
}
