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
	"sort"
	"strconv"
	"strings"
	"time"

	"go.temporal.io/sdk/client"

	"github.com/dstout-devops/stratt/core/internal/authz"
	"github.com/dstout-devops/stratt/core/internal/awxfacade"
	"github.com/dstout-devops/stratt/core/internal/compiler"
	"github.com/dstout-devops/stratt/core/internal/contract"
	"github.com/dstout-devops/stratt/core/internal/desiredstate"
	"github.com/dstout-devops/stratt/core/internal/events"
	"github.com/dstout-devops/stratt/core/internal/evidencestore"
	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/mcpserver"
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
	// CompileStatus, when set, is the shared Intent-compile status the
	// controller updates and GET /compile serves (ADR-0023).
	CompileStatus *compiler.Status
	// Evidence, when set, serves sealed audit bundles (§2.4, ADR-0029). Nil
	// (no object store configured) makes the Evidence endpoints 404.
	Evidence *evidencestore.Store
	// SiteLiveness, when set, returns the set of Sites whose agent is currently
	// heartbeating (ADR-0032) — read from the NATS liveness KV, never the graph
	// (§1.2). Nil reports every Site as not-live.
	SiteLiveness func(ctx context.Context) (map[string]bool, error)
}

// Handler mounts the generated routes under /api/v1, behind the Principal
// resolver — one identity seam for every surface (§1.6, ADR-0009).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// principal resolves identity; audit records every request behind it (the
	// full access log, §1.6) — audit is INNER so it sees the resolved Principal.
	mux.Handle("/api/v1/", http.StripPrefix("/api/v1", s.principalMiddleware(s.auditMiddleware(Handler(s)))))
	// Platform MCP server (§1.6, ADR-0021): the agent surface, same identity
	// seam (ResolvePrincipal), same capabilities (tools invoke the generated
	// router in-process). Never anonymous — 401 without a Principal. Tool calls
	// fold into the one audit stream as mcp.tool-call events.
	mux.Handle("/mcp", mcpserver.New(mcpserver.Config{
		Resolve:     s.ResolvePrincipal,
		API:         Handler(s),
		RecordUsage: s.recordMCPAudit,
		Log:         s.Log,
	}))
	// AWX-compatible /api/v2 façade (§5.6, ADR-0026): a stateless compat
	// surface over the same seams, for tooling cutover. Its own auth middleware
	// (Bearer/Basic/dev); definitions read-only, launch+cancel the only writes.
	mux.Handle("/api/v2/", awxfacade.New(awxfacade.Config{
		Store:              s.Store,
		Bus:                s.Bus,
		Temporal:           s.Temporal,
		Authz:              s.Authz,
		OIDC:               s.OIDC,
		DevPrincipalHeader: s.DevPrincipalHeader,
		Log:                s.Log,
	}))
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

// auditMiddleware records every request into the one audit stream (§1.6,
// ADR-0034) after it runs — the full access log, reads included. It runs
// behind principalMiddleware so the acting Principal is on the context. The
// append is best-effort on a background context (the response is already
// served); a failure is logged, never hidden (§1.8). Action is the matched
// route pattern (low-cardinality); Object is the concrete path.
func (s *Server) auditMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		id, kind, _ := authz.PrincipalFrom(r.Context())
		action := r.Method + " " + r.URL.Path
		if r.Pattern != "" {
			action = r.Pattern
		}
		if err := s.Store.RecordAudit(context.WithoutCancel(r.Context()), types.AuditEvent{
			PrincipalID: id, PrincipalKind: kind, Action: action,
			Object: r.URL.Path, Outcome: strconv.Itoa(rec.status),
		}); err != nil {
			s.Log.Error("audit record failed", "action", action, "err", err)
		}
	})
}

// statusRecorder captures the response status for the audit log.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status, r.wrote = code, true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(b)
}

// Flush lets the SSE run-event stream keep working through the wrapper.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// recordMCPAudit folds a platform-MCP tool invocation into the one audit
// stream (§1.6) as an mcp.tool-call event — replacing the standalone
// audit.mcp_call write so GET /usage and the SIEM forwarder see the same rows.
func (s *Server) recordMCPAudit(ctx context.Context, c types.MCPCall) error {
	outcome := types.AuditOK
	if !c.OK {
		outcome = types.AuditFailed
	}
	detail, _ := json.Marshal(map[string]any{"durationMs": c.DurationMS})
	return s.Store.RecordAudit(ctx, types.AuditEvent{
		PrincipalID: c.Principal, PrincipalKind: c.PrincipalKind,
		Action: types.AuditMCPToolCall, Object: c.Tool, Outcome: outcome, Detail: detail,
	})
}

// principalMiddleware resolves the request Principal. Bearer tokens go to
// the OIDC resolver when configured — an invalid or expired token is 401,
// never a silent downgrade to anonymous; the dev header requires explicit
// opt-in. Anonymous requests proceed unresolved — endpoints that require a
// grant deny them (default deny lives at the check, not here).
func (s *Server) principalMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, kind, err := s.ResolvePrincipal(r.Context(), r.Header)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "invalid bearer token")
			return
		}
		if id != "" {
			r = r.WithContext(authz.WithPrincipal(r.Context(), id, kind))
		}
		next.ServeHTTP(w, r)
	})
}

// ResolvePrincipal maps request headers to a Principal — the ONE identity
// seam every surface shares (§1.6, ADR-0009): REST middleware and the MCP
// tool layer both call it. ("", "", nil) means anonymous — default deny
// lives at the checks; an invalid Bearer token errors, never downgrades.
func (s *Server) ResolvePrincipal(ctx context.Context, h http.Header) (id, kind string, err error) {
	if h == nil {
		return "", "", nil
	}
	if auth := h.Get("Authorization"); s.OIDC != nil && strings.HasPrefix(auth, "Bearer ") {
		return s.OIDC.Resolve(ctx, strings.TrimPrefix(auth, "Bearer "))
	}
	if s.DevPrincipalHeader {
		if id := h.Get("X-Stratt-Principal"); id != "" {
			kind := h.Get("X-Stratt-Principal-Kind")
			if kind == "" {
				kind = authz.KindHuman
			}
			return id, kind, nil
		}
	}
	return "", "", nil
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
	if len(r.Outputs) > 0 {
		var m map[string]any
		if json.Unmarshal(r.Outputs, &m) == nil {
			out.Outputs = &m
		}
	}
	if len(r.Sites) > 0 {
		sites := append([]string(nil), r.Sites...)
		out.Sites = &sites
	}
	return out
}

// siteToWire renders a Site declaration with its live agent status (ADR-0032).
func siteToWire(s types.Site, live bool) Site {
	out := Site{Name: s.Name, Mode: SiteMode(s.Mode), Live: &live}
	if s.Namespace != "" {
		out.Namespace = &s.Namespace
	}
	if s.Description != "" {
		out.Description = &s.Description
	}
	if s.DeclaredBy != "" {
		db := SiteDeclaredBy(s.DeclaredBy)
		out.DeclaredBy = &db
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
	if t.ViewParams != nil {
		out.ViewParams = &t.ViewParams
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

// jsonRoundTrip re-encodes a wire object and decodes it into a domain type —
// used where the two share a JSON shape (ADR-0023 intent-layer kinds).
func jsonRoundTrip(from, to any) error {
	raw, err := json.Marshal(from)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, to)
}

func baselineToWire(b types.Baseline) Baseline {
	out := Baseline{Name: b.Name, ViewName: b.ViewName, Cron: b.Cron, Severity: BaselineSeverity(b.Severity)}
	if b.Mode != "" {
		out.Mode = &b.Mode
	}
	if b.CompiledFrom != nil {
		out.CompiledFrom = &struct {
			Assignment       *string `json:"assignment,omitempty"`
			Blueprint        *string `json:"blueprint,omitempty"`
			BlueprintVersion *int64  `json:"blueprintVersion,omitempty"`
			Intent           *string `json:"intent,omitempty"`
			Route            *int64  `json:"route,omitempty"`
		}{}
		out.CompiledFrom.Assignment = &b.CompiledFrom.Assignment
		out.CompiledFrom.Intent = &b.CompiledFrom.Intent
		out.CompiledFrom.Blueprint = &b.CompiledFrom.Blueprint
		v := int64(b.CompiledFrom.BlueprintVersion)
		out.CompiledFrom.BlueprintVersion = &v
		r := int64(b.CompiledFrom.Route)
		out.CompiledFrom.Route = &r
	}
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
	// Intent-layer kinds (ADR-0023): the wire shapes are JSON-equivalent to
	// the domain types (the CLI marshals desiredstate.Declarations directly),
	// so a JSON round-trip is the faithful conversion — it preserves the
	// route observe equals/contains raw values through the generated
	// interface{} fields.
	if in.Intents != nil {
		for _, w := range *in.Intents {
			var it types.Intent
			if err := jsonRoundTrip(w, &it); err != nil {
				return out, fmt.Errorf("intent %s: %w", w.Name, err)
			}
			if err := desiredstate.ValidateIntent(it); err != nil {
				return out, err
			}
			out.Intents = append(out.Intents, it)
		}
	}
	if in.Assignments != nil {
		for _, w := range *in.Assignments {
			var a types.Assignment
			if err := jsonRoundTrip(w, &a); err != nil {
				return out, fmt.Errorf("assignment %s: %w", w.Name, err)
			}
			if err := desiredstate.ValidateAssignment(a); err != nil {
				return out, err
			}
			out.Assignments = append(out.Assignments, a)
		}
	}
	if in.Blueprints != nil {
		for _, w := range *in.Blueprints {
			var b types.Blueprint
			if err := jsonRoundTrip(w, &b); err != nil {
				return out, fmt.Errorf("blueprint %s: %w", w.Name, err)
			}
			if err := desiredstate.ValidateBlueprint(b); err != nil {
				return out, err
			}
			out.Blueprints = append(out.Blueprints, b)
		}
	}
	if in.McpServers != nil {
		for _, w := range *in.McpServers {
			m := types.MCPServer{Name: w.Name, Transport: string(w.Transport), Rev: int(w.Rev)}
			if w.Script != nil {
				m.Script = *w.Script
			}
			if w.Endpoint != nil {
				m.Endpoint = *w.Endpoint
			}
			if w.TokenRef != nil {
				m.TokenRef = &types.MCPTokenRef{CredentialRef: w.TokenRef.CredentialRef, Key: w.TokenRef.Key}
			}
			if err := desiredstate.ValidateMCPServer(m); err != nil {
				return out, fmt.Errorf("mcp server %s: %w", w.Name, err)
			}
			out.MCPServers = append(out.MCPServers, m)
		}
	}
	if in.Sites != nil {
		for _, w := range *in.Sites {
			st := types.Site{Name: w.Name, Mode: string(w.Mode), DeclaredBy: "cac"}
			if w.Namespace != nil {
				st.Namespace = *w.Namespace
			}
			if w.Description != nil {
				st.Description = *w.Description
			}
			if err := desiredstate.ValidateSite(st); err != nil {
				return out, fmt.Errorf("site %s: %w", w.Name, err)
			}
			out.Sites = append(out.Sites, st)
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
	if w.ViewParams != nil {
		t.ViewParams = *w.ViewParams
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
		if e.ParamDependent {
			w.ParamDependent = &e.ParamDependent
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
	v, ents, err := s.Store.ResolveView(r.Context(), name, nil, limit)
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
	// A targetless Connector Action (§2.2, ADR-0031): no View, so no
	// runner-on-View grant — the credential `use`-check in RunAction is the
	// authz chokepoint. Validate the input Contract at the door.
	if body.Action != nil && *body.Action != "" {
		s.startAction(w, r, body)
		return
	}
	if body.ViewName == nil || *body.ViewName == "" {
		writeErr(w, http.StatusBadRequest, "viewName is required for an actuator Run")
		return
	}
	// Contract check at the door (§1.5, ADR-0015): a malformed Step fails
	// here with pointer detail — before any Run row exists, not at dispatch.
	if err := validateStepParams(body.Actuator, body.Params); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// View-scoped execution authz (§2.5, ADR-0028): the launching Principal must
	// hold `runner` on the target View — fail fast at the door with a 403 (the
	// RunAgainstView chokepoint re-checks, covering the no-handler paths).
	if !s.requireGrant(w, r, authz.RelationRunner, "view:"+*body.ViewName) {
		return
	}
	p := orchestrate.LaunchParams{ViewName: *body.ViewName}
	// The launching Principal rides the Run for the dispatch-time `use`
	// check and the audit trail (§1.8). Anonymous launches carry none and
	// fail credential resolution if refs are requested.
	if id, _, ok := authz.PrincipalFrom(r.Context()); ok {
		p.Principal = id
	}
	if body.CredentialRefs != nil {
		p.CredentialRefs = *body.CredentialRefs
	}
	if body.Actuator != nil {
		if !body.Actuator.Valid() {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("unknown actuator %q", *body.Actuator))
			return
		}
		p.Actuator = string(*body.Actuator)
	}
	if body.Params != nil {
		raw, err := json.Marshal(*body.Params)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid params: "+err.Error())
			return
		}
		p.Params = raw
	}
	if body.Slices != nil {
		if *body.Slices < 1 {
			writeErr(w, http.StatusBadRequest, "slices must be >= 1")
			return
		}
		p.Slices = int(*body.Slices)
	}
	run, err := orchestrate.LaunchRun(r.Context(), orchestrate.LaunchDeps{Store: s.Store, Temporal: s.Temporal}, p)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, runToWire(run))
}

// startAction launches a targetless Connector Action (§2.2, ADR-0031). Input
// validated at the door; authz is the CredentialRef `use`-check inside
// RunAction (Actions are not View-scoped).
func (s *Server) startAction(w http.ResponseWriter, r *http.Request, body StartRun) {
	var params json.RawMessage
	if body.Params != nil {
		raw, err := json.Marshal(*body.Params)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid params: "+err.Error())
			return
		}
		params = raw
	}
	if err := contract.ValidateActionInput(*body.Action, params); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	p := orchestrate.LaunchParams{Action: *body.Action, Params: params}
	if body.DryRun != nil {
		p.DryRun = *body.DryRun
	}
	if id, _, ok := authz.PrincipalFrom(r.Context()); ok {
		p.Principal = id
	}
	if body.CredentialRefs != nil {
		p.CredentialRefs = *body.CredentialRefs
	}
	run, err := orchestrate.LaunchRun(r.Context(), orchestrate.LaunchDeps{Store: s.Store, Temporal: s.Temporal}, p)
	if err != nil {
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

// CancelRun implements (POST /runs/{id}/cancel): signal the Run's Temporal
// workflow to cancel. The Workflow owns the canceled transition and Job
// cleanup (ADR-0026); this only requests it. 202 even for a terminal Run
// (idempotent, AWX-cancel semantics).
//
// Authorization is View-scoped (§2.5, ADR-0028): the caller must hold `runner`
// on the Run's View — you may cancel Runs against Views you may launch against.
// This re-introduces the object-gating ADR-0026 deferred (the `view` type now
// exists); launch and cancel share the one runner relation, so the /api/v2
// façade cannot be a weaker path (§1.6 symmetry).
func (s *Server) CancelRun(w http.ResponseWriter, r *http.Request, id RunID) {
	run, err := s.Store.GetRun(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !s.requireGrant(w, r, authz.RelationRunner, "view:"+viewNameFromRef(run.ViewRef)) {
		return
	}
	if err := orchestrate.CancelRun(r.Context(), s.Temporal, run.ID); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// viewNameFromRef strips the "view://" scheme from a Run's ViewRef, yielding the
// bare View name used as the authz object (view:<name>).
func viewNameFromRef(ref string) string {
	return strings.TrimPrefix(ref, "view://")
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
	wf, err := s.Store.GetWorkflow(r.Context(), name)
	if err != nil {
		s.fail(w, err)
		return
	}
	// View-scoped execution authz (§2.5, ADR-0028): the launching Principal must
	// hold `runner` on EVERY actuation Step's View (gate Steps target none). The
	// per-Step child Runs re-check at the RunAgainstView chokepoint; this is the
	// fail-fast 403 at the door.
	for _, st := range wf.Steps {
		if st.ViewName == "" {
			continue
		}
		if !s.requireGrant(w, r, authz.RelationRunner, "view:"+st.ViewName) {
			return
		}
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

// liveSites reads the current heartbeat set (ADR-0032); a nil provider or an
// error means "no live status available" — the declaration still returns.
func (s *Server) liveSites(ctx context.Context) map[string]bool {
	if s.SiteLiveness == nil {
		return nil
	}
	live, err := s.SiteLiveness(ctx)
	if err != nil {
		s.Log.Warn("site liveness read failed", "err", err)
		return nil
	}
	return live
}

// ListSites implements (GET /sites): declared Sites merged with live agent
// status (ADR-0032).
func (s *Server) ListSites(w http.ResponseWriter, r *http.Request) {
	sites, err := s.Store.ListSites(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	live := s.liveSites(r.Context())
	out := make([]Site, 0, len(sites))
	for _, st := range sites {
		out = append(out, siteToWire(st, live[st.Name]))
	}
	writeJSON(w, http.StatusOK, out)
}

// GetSite implements (GET /sites/{name}).
func (s *Server) GetSite(w http.ResponseWriter, r *http.Request, name string) {
	st, err := s.Store.GetSite(r.Context(), name)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, siteToWire(st, s.liveSites(r.Context())[name]))
}

// ListUsage implements (GET /usage): the §1.6 per-identity MCP accounting
// aggregate (ADR-0021).
func (s *Server) ListUsage(w http.ResponseWriter, r *http.Request, params ListUsageParams) {
	principal := ""
	if params.Principal != nil {
		principal = *params.Principal
	}
	us, err := s.Store.ListUsage(r.Context(), principal)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]UsageEntry, 0, len(us))
	for _, u := range us {
		e := UsageEntry{Principal: u.Principal, Tool: u.Tool, Calls: u.Calls, Errors: u.Errors, LastCall: u.LastCall}
		if u.PrincipalKind != "" {
			e.PrincipalKind = &u.PrincipalKind
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, out)
}

// ListAudit implements (GET /audit): the one audit stream (§1.6, ADR-0034),
// cursor-paged by seq. Privileged — a reader grant on audit:log, deny-by-
// default (unlike v1's open read endpoints), because who-did-what-when is
// sensitive.
func (s *Server) ListAudit(w http.ResponseWriter, r *http.Request, params ListAuditParams) {
	if !s.requireGrant(w, r, authz.RelationReader, authz.AuditObject) {
		return
	}
	since, principal, action, limit := int64(0), "", "", 0
	if params.Since != nil {
		since = *params.Since
	}
	if params.Principal != nil {
		principal = *params.Principal
	}
	if params.Action != nil {
		action = *params.Action
	}
	if params.Limit != nil {
		limit = int(*params.Limit)
	}
	evs, err := s.Store.ListAudit(r.Context(), principal, action, since, limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]AuditEvent, 0, len(evs))
	for _, e := range evs {
		out = append(out, auditToWire(e))
	}
	writeJSON(w, http.StatusOK, out)
}

// VerifyAudit implements (GET /audit/verify): walk the tamper-evidence hash
// chain and report integrity (§1.8, ADR-0034).
func (s *Server) VerifyAudit(w http.ResponseWriter, r *http.Request) {
	if !s.requireGrant(w, r, authz.RelationReader, authz.AuditObject) {
		return
	}
	v, err := s.Store.VerifyAudit(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	out := AuditVerification{Ok: v.OK, SealedThrough: v.SealedThrough, Events: v.Events}
	if v.FirstBadSeq != 0 {
		out.FirstBadSeq = &v.FirstBadSeq
	}
	if v.Reason != "" {
		out.Reason = &v.Reason
	}
	writeJSON(w, http.StatusOK, out)
}

// GetForwardConfig implements (GET /audit/forward/{sink}/config): the declared
// SIEM Sink's non-secret egress config, so the forwarder treats the CaC Sink as
// the source of truth (ADR-0034). Never returns the credential (§2.5).
func (s *Server) GetForwardConfig(w http.ResponseWriter, r *http.Request, sink string) {
	if !s.requireGrant(w, r, authz.RelationForwarder, authz.AuditObject) {
		return
	}
	sk, err := s.Store.GetNotifySink(r.Context(), sink)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !types.SIEMSinkKinds[sk.Kind] {
		writeErr(w, http.StatusNotFound, "sink "+sink+" is not a SIEM audit sink")
		return
	}
	out := ForwardConfig{Sink: sk.Name, Kind: sk.Kind, Endpoint: sk.Config.Endpoint}
	if sk.Config.Index != "" {
		out.Index = &sk.Config.Index
	}
	if sk.Config.Facility != 0 {
		f := sk.Config.Facility
		out.Facility = &f
	}
	if sk.Config.Insecure {
		ins := true
		out.Insecure = &ins
	}
	writeJSON(w, http.StatusOK, out)
}

// GetForwardBatch implements (GET /audit/forward/{sink}): the next in-order
// audit batch after the sink's committed offset — the at-least-once egress read
// (ADR-0034). The server owns the cursor; repeated calls return the same batch
// until the forwarder reports delivery.
func (s *Server) GetForwardBatch(w http.ResponseWriter, r *http.Request, sink string, params GetForwardBatchParams) {
	if !s.requireGrant(w, r, authz.RelationForwarder, authz.AuditObject) {
		return
	}
	offset, err := s.Store.GetForwardOffset(r.Context(), sink)
	if err != nil {
		s.fail(w, err)
		return
	}
	limit := 0
	if params.Limit != nil {
		limit = int(*params.Limit)
	}
	evs, err := s.Store.ForwardBatch(r.Context(), offset, limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]AuditEvent, 0, len(evs))
	for _, e := range evs {
		out = append(out, auditToWire(e))
	}
	writeJSON(w, http.StatusOK, out)
}

// ReportForward implements (POST /audit/forward/{sink}/report): the forwarder's
// delivery outcome. "delivered" commits the offset (forward-only) and records
// the delivery; "failed" records the failure but never advances the offset, so
// the batch re-ships until it lands — a dropped audit record is impossible by
// design (ADR-0034, §1.8).
func (s *Server) ReportForward(w http.ResponseWriter, r *http.Request, sink string) {
	if !s.requireGrant(w, r, authz.RelationForwarder, authz.AuditObject) {
		return
	}
	var body ForwardReport
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid report")
		return
	}
	if body.Status == ForwardReportStatusDelivered {
		if err := s.Store.CommitForwardOffset(r.Context(), sink, body.ThroughSeq); err != nil {
			s.fail(w, err)
			return
		}
	}
	detail := ""
	if body.Detail != nil {
		detail = *body.Detail
	}
	if err := s.Store.RecordForwardDelivery(r.Context(), types.ForwardDelivery{
		Sink: sink, ThroughSeq: body.ThroughSeq, Count: body.Count,
		Status: string(body.Status), Detail: detail,
	}); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListForwardDeliveries implements (GET /audit/forward/{sink}/deliveries).
func (s *Server) ListForwardDeliveries(w http.ResponseWriter, r *http.Request, sink string) {
	if !s.requireGrant(w, r, authz.RelationForwarder, authz.AuditObject) {
		return
	}
	ds, err := s.Store.ListForwardDeliveries(r.Context(), sink, 0)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]ForwardDelivery, 0, len(ds))
	for _, d := range ds {
		wire := ForwardDelivery{Sink: d.Sink, ThroughSeq: d.ThroughSeq, Count: d.Count, Status: d.Status, At: d.At}
		if d.Detail != "" {
			wire.Detail = &d.Detail
		}
		out = append(out, wire)
	}
	writeJSON(w, http.StatusOK, out)
}

func auditToWire(e types.AuditEvent) AuditEvent {
	a := AuditEvent{Seq: e.Seq, At: e.At, Action: e.Action}
	if e.PrincipalID != "" {
		a.PrincipalId = &e.PrincipalID
	}
	if e.PrincipalKind != "" {
		a.PrincipalKind = &e.PrincipalKind
	}
	if e.Object != "" {
		a.Object = &e.Object
	}
	if e.Outcome != "" {
		a.Outcome = &e.Outcome
	}
	if len(e.Detail) > 0 {
		var d any
		if json.Unmarshal(e.Detail, &d) == nil {
			a.Detail = d
		}
	}
	sealed := e.Hash != nil
	a.Sealed = &sealed
	return a
}

// ListIntents implements (GET /intents): declared Intents (CaC-only).
func (s *Server) ListIntents(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListIntents(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if items == nil {
		items = []types.Intent{}
	}
	writeJSON(w, http.StatusOK, items)
}

// ListAssignments implements (GET /assignments): declared Assignments.
func (s *Server) ListAssignments(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListAssignments(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if items == nil {
		items = []types.Assignment{}
	}
	writeJSON(w, http.StatusOK, items)
}

// ListBlueprints implements (GET /blueprints): declared Blueprint versions.
func (s *Server) ListBlueprints(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListBlueprints(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if items == nil {
		items = []types.Blueprint{}
	}
	writeJSON(w, http.StatusOK, items)
}

// GetCompileStatus implements (GET /compile): the latest Intent-compile pass
// summary (§4.3 membership-delta surface, ADR-0023).
func (s *Server) GetCompileStatus(w http.ResponseWriter, _ *http.Request) {
	if s.CompileStatus == nil {
		writeJSON(w, http.StatusOK, compiler.Snapshot{})
		return
	}
	writeJSON(w, http.StatusOK, s.CompileStatus.Get())
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

// GetComplianceReport implements (GET /compliance/{framework}): the framework's
// per-View posture score (ADR-0033). It folds the framework-tagged Baselines
// (the controls) against their open Findings — a control passes when no target
// in its View has an open Finding. Read-only over the existing surface; the
// benchmark is data, a pack ships the controls.
func (s *Server) GetComplianceReport(w http.ResponseWriter, r *http.Request, framework string, params GetComplianceReportParams) {
	viewFilter := ""
	if params.View != nil {
		viewFilter = *params.View
	}
	baselines, err := s.Store.ListBaselines(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	openCounts, err := s.Store.OpenFindingCountsByFramework(r.Context(), framework)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, buildComplianceReport(framework, viewFilter, baselines, openCounts))
}

// buildComplianceReport folds the framework-tagged Baselines (the controls)
// against their open-Finding counts into a per-View score (ADR-0033). A control
// passes when it has no open Findings. Pure over its inputs — the I/O lives in
// the handler — so the scoring is unit-tested without a store.
func buildComplianceReport(framework, viewFilter string, baselines []types.Baseline, openCounts map[string]int) ComplianceReport {
	type acc struct {
		controls, passing, failing int64
		failingControls            []FailingControl
	}
	byView := map[string]*acc{}
	var order []string
	for _, b := range baselines {
		if b.Framework != framework {
			continue
		}
		if viewFilter != "" && b.ViewName != viewFilter {
			continue
		}
		a := byView[b.ViewName]
		if a == nil {
			a = &acc{}
			byView[b.ViewName] = a
			order = append(order, b.ViewName)
		}
		a.controls++
		if n := openCounts[b.Name]; n > 0 {
			a.failing++
			a.failingControls = append(a.failingControls, FailingControl{
				Baseline:     b.Name,
				Severity:     FailingControlSeverity(b.Severity),
				OpenFindings: int64(n),
			})
		} else {
			a.passing++
		}
	}
	sort.Strings(order)

	report := ComplianceReport{Framework: framework, Views: make([]ComplianceViewScore, 0, len(order))}
	for _, view := range order {
		a := byView[view]
		score := 1.0
		if a.controls > 0 {
			score = float64(a.passing) / float64(a.controls)
		}
		sort.Slice(a.failingControls, func(i, j int) bool {
			return a.failingControls[i].Baseline < a.failingControls[j].Baseline
		})
		vs := ComplianceViewScore{
			View: view, Controls: a.controls, Passing: a.passing, Failing: a.failing, Score: score,
		}
		if len(a.failingControls) > 0 {
			vs.FailingControls = &a.failingControls
		}
		report.Views = append(report.Views, vs)
	}
	return report
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

func evidenceToWire(e types.Evidence) Evidence {
	return Evidence{
		Id: e.ID, FindingId: e.FindingID, Baseline: e.Baseline, Target: e.Target,
		ObjectKey: e.ObjectKey, Sha256: e.SHA256, SizeBytes: e.SizeBytes,
		SealedAt: e.SealedAt, RetainUntil: e.RetainUntil,
	}
}

// GetFindingEvidence implements (GET /findings/{id}/evidence): the manifest for
// the Finding's sealed audit bundle (§2.4, ADR-0029).
func (s *Server) GetFindingEvidence(w http.ResponseWriter, r *http.Request, id string) {
	e, err := s.Store.GetEvidenceByFinding(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, evidenceToWire(e))
}

// DownloadEvidence implements (GET /evidence/{id}/download): stream the sealed
// bundle after re-verifying its sha256 against the manifest — a tampered object
// is refused with 409, never served as authentic (§1.8, ADR-0029).
func (s *Server) DownloadEvidence(w http.ResponseWriter, r *http.Request, id string) {
	e, err := s.Store.GetEvidence(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if s.Evidence == nil {
		writeErr(w, http.StatusNotFound, "evidence store not configured")
		return
	}
	body, err := s.Evidence.GetVerified(r.Context(), e.ObjectKey, e.SHA256)
	if errors.Is(err, evidencestore.ErrTampered) {
		writeErr(w, http.StatusConflict, "evidence object failed its integrity check (tampered)")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Stratt-Evidence-SHA256", e.SHA256)
	w.Header().Set("Content-Disposition", "attachment; filename=evidence-"+e.FindingID+".json")
	_, _ = w.Write(body)
}
