// Package scim is Stratt's SCIM 2.0 Service Provider (charter §8, ADR-0035):
// the customer's IdP (Okta/Entra/Zitadel) PUSHES Users and Groups here, and the
// provider projects them into the born-here identity registry (the `scim`
// schema, graph.scimstore). Mounted OUTSIDE /api/v1 — the IdP authenticates by a
// bearer token whose CaC declaration holds only its sha256 (§2.5), exactly like
// an Emitter; it is not a Principal.
//
// The registry BACKS the one Principal model (§1.6): a provisioned identity's
// principal_id is the value the OIDC `sub` carries at request time. Every
// provisioning mutation is recorded into the one audit stream (ADR-0034), so
// offboarding is itself auditable and SIEM-forwardable. SCIM is plain JSON over
// REST — the wire formats are hand-rolled (no SCIM library, no new dependency),
// mirroring the hand-rolled SIEM drivers.
package scim

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

const (
	schemaUser    = "urn:ietf:params:scim:schemas:core:2.0:User"
	schemaGroup   = "urn:ietf:params:scim:schemas:core:2.0:Group"
	schemaList    = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	schemaError   = "urn:ietf:params:scim:api:messages:2.0:Error"
	schemaPatchOp = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	contentType   = "application/scim+json"
)

// Provider serves /scim/v2. The graph Store is the sole writer of the projection
// (scim.identity/group/group_member) and the CaC config reader (scim.idp).
type Provider struct {
	store *graph.Store
	log   *slog.Logger
}

// New builds the SCIM provider.
func New(store *graph.Store, log *slog.Logger) *Provider {
	return &Provider{store: store, log: log.With("component", "scim")}
}

// Handler serves the SCIM 2.0 core surface under /scim/v2.
func (p *Provider) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Discovery endpoints are read-only descriptors; Okta/Entra probe them
		// before provisioning. Still token-gated (the SP is not public).
		idp, ok := p.authenticate(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/scim/v2"), "/")
		seg := strings.SplitN(rest, "/", 2)
		resource := seg[0]
		var id string
		if len(seg) == 2 {
			id = seg[1]
		}
		switch resource {
		case "ServiceProviderConfig":
			writeJSON(w, http.StatusOK, serviceProviderConfig())
		case "ResourceTypes":
			writeJSON(w, http.StatusOK, resourceTypes())
		case "Schemas":
			writeJSON(w, http.StatusOK, schemasDoc())
		case "Users":
			p.users(w, r, idp, id)
		case "Groups":
			p.groups(w, r, idp, id)
		default:
			writeError(w, http.StatusNotFound, "unknown resource")
		}
	})
}

// authenticate resolves the pushing IdP BY its bearer token: the token both
// authenticates and identifies which registered IdP this push belongs to
// (multi-IdP by construction). Constant-time compare against every declared
// token hash; no match = 401.
func (p *Provider) authenticate(r *http.Request) (string, bool) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return "", false
	}
	token := strings.TrimPrefix(auth, "Bearer ")
	if token == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(token))
	presented := []byte(hex.EncodeToString(sum[:]))
	idps, err := p.store.ListIDPs(r.Context())
	if err != nil {
		p.log.Error("scim list idps failed", "err", err)
		return "", false
	}
	for _, d := range idps {
		if subtle.ConstantTimeCompare(presented, []byte(strings.ToLower(d.TokenHash))) == 1 {
			return d.Name, true
		}
	}
	return "", false
}

// ── Users ─────────────────────────────────────────────────────────────────────

func (p *Provider) users(w http.ResponseWriter, r *http.Request, idp, id string) {
	switch {
	case id == "" && r.Method == http.MethodGet:
		p.listUsers(w, r, idp)
	case id == "" && r.Method == http.MethodPost:
		p.createUser(w, r, idp)
	case id != "" && r.Method == http.MethodGet:
		p.getUser(w, r, idp, id)
	case id != "" && (r.Method == http.MethodPut):
		p.replaceUser(w, r, idp, id)
	case id != "" && r.Method == http.MethodPatch:
		p.patchUser(w, r, idp, id)
	case id != "" && r.Method == http.MethodDelete:
		p.deleteUser(w, r, idp, id)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (p *Provider) listUsers(w http.ResponseWriter, r *http.Request, idp string) {
	field, value := parseFilter(r.URL.Query().Get("filter"))
	if field != "" && field != "userName" && field != "externalId" {
		writeError(w, http.StatusBadRequest, "unsupported filter attribute")
		return
	}
	ids, err := p.store.ListIdentities(r.Context(), idp, field, value)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	res := make([]any, 0, len(ids))
	for _, i := range ids {
		res = append(res, userResource(i))
	}
	writeJSON(w, http.StatusOK, listResponse(res))
}

func (p *Provider) getUser(w http.ResponseWriter, r *http.Request, idp, id string) {
	i, err := p.store.GetIdentity(r.Context(), idp, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, userResource(i))
}

func (p *Provider) createUser(w http.ResponseWriter, r *http.Request, idp string) {
	body, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body")
		return
	}
	var u userWire
	if err := json.Unmarshal(body, &u); err != nil {
		writeError(w, http.StatusBadRequest, "invalid SCIM user")
		return
	}
	if u.UserName == "" {
		writeError(w, http.StatusBadRequest, "userName required")
		return
	}
	i := types.SCIMIdentity{
		IDP:         idp,
		SCIMID:      newID(),
		UserName:    u.UserName,
		ExternalID:  u.ExternalID,
		PrincipalID: principalID(u),
		Active:      u.activeOrDefault(),
		Emails:      u.Emails,
	}
	if err := p.store.UpsertIdentity(r.Context(), i); err != nil {
		writeError(w, http.StatusInternalServerError, "create failed")
		return
	}
	p.audit(r.Context(), idp, "scim.user.provision", i.PrincipalID, types.AuditOK)
	got, _ := p.store.GetIdentity(r.Context(), idp, i.SCIMID)
	writeJSON(w, http.StatusCreated, userResource(got))
}

func (p *Provider) replaceUser(w http.ResponseWriter, r *http.Request, idp, id string) {
	cur, err := p.store.GetIdentity(r.Context(), idp, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	body, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body")
		return
	}
	var u userWire
	if err := json.Unmarshal(body, &u); err != nil {
		writeError(w, http.StatusBadRequest, "invalid SCIM user")
		return
	}
	wasActive := cur.Active
	cur.UserName = u.UserName
	cur.ExternalID = u.ExternalID
	cur.PrincipalID = principalID(u)
	cur.Active = u.activeOrDefault()
	if u.Emails != nil {
		cur.Emails = u.Emails
	}
	if err := p.store.UpsertIdentity(r.Context(), cur); err != nil {
		writeError(w, http.StatusInternalServerError, "replace failed")
		return
	}
	p.auditActive(r.Context(), idp, cur.PrincipalID, wasActive, cur.Active, "scim.user.update")
	got, _ := p.store.GetIdentity(r.Context(), idp, id)
	writeJSON(w, http.StatusOK, userResource(got))
}

func (p *Provider) patchUser(w http.ResponseWriter, r *http.Request, idp, id string) {
	cur, err := p.store.GetIdentity(r.Context(), idp, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	body, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body")
		return
	}
	var patch patchWire
	if err := json.Unmarshal(body, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid PatchOp")
		return
	}
	newActive, changed := patch.activeChange()
	if !changed {
		// No active change we recognize — accept as a no-op (idempotent PATCH).
		writeJSON(w, http.StatusOK, userResource(cur))
		return
	}
	if err := p.store.SetIdentityActive(r.Context(), idp, id, newActive); err != nil {
		writeError(w, http.StatusInternalServerError, "patch failed")
		return
	}
	action := "scim.user.update"
	if !newActive {
		action = "scim.user.deactivate"
	}
	p.audit(r.Context(), idp, action, cur.PrincipalID, types.AuditOK)
	got, _ := p.store.GetIdentity(r.Context(), idp, id)
	writeJSON(w, http.StatusOK, userResource(got))
}

func (p *Provider) deleteUser(w http.ResponseWriter, r *http.Request, idp, id string) {
	cur, err := p.store.GetIdentity(r.Context(), idp, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err := p.store.DeleteIdentity(r.Context(), idp, id); err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	p.audit(r.Context(), idp, "scim.user.delete", cur.PrincipalID, types.AuditOK)
	w.WriteHeader(http.StatusNoContent)
}

// ── Groups ──────────────────────────────────────────────────────────────────

func (p *Provider) groups(w http.ResponseWriter, r *http.Request, idp, id string) {
	switch {
	case id == "" && r.Method == http.MethodGet:
		p.listGroups(w, r, idp)
	case id == "" && r.Method == http.MethodPost:
		p.createGroup(w, r, idp)
	case id != "" && r.Method == http.MethodGet:
		p.getGroup(w, r, idp, id)
	case id != "" && r.Method == http.MethodPut:
		p.replaceGroup(w, r, idp, id)
	case id != "" && r.Method == http.MethodPatch:
		p.patchGroup(w, r, idp, id)
	case id != "" && r.Method == http.MethodDelete:
		p.deleteGroup(w, r, idp, id)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (p *Provider) listGroups(w http.ResponseWriter, r *http.Request, idp string) {
	field, value := parseFilter(r.URL.Query().Get("filter"))
	if field != "" && field != "displayName" {
		writeError(w, http.StatusBadRequest, "unsupported filter attribute")
		return
	}
	gs, err := p.store.ListGroups(r.Context(), idp, field, value)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	res := make([]any, 0, len(gs))
	for _, g := range gs {
		members, _ := p.store.GroupMembers(r.Context(), idp, g.SCIMID)
		res = append(res, groupResource(g, members))
	}
	writeJSON(w, http.StatusOK, listResponse(res))
}

func (p *Provider) getGroup(w http.ResponseWriter, r *http.Request, idp, id string) {
	g, members, err := p.store.GetGroup(r.Context(), idp, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "group not found")
		return
	}
	writeJSON(w, http.StatusOK, groupResource(g, members))
}

func (p *Provider) createGroup(w http.ResponseWriter, r *http.Request, idp string) {
	body, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body")
		return
	}
	var gw groupWire
	if err := json.Unmarshal(body, &gw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid SCIM group")
		return
	}
	if gw.DisplayName == "" {
		writeError(w, http.StatusBadRequest, "displayName required")
		return
	}
	g := types.SCIMGroup{IDP: idp, SCIMID: newID(), DisplayName: gw.DisplayName, ExternalID: gw.ExternalID}
	if err := p.store.UpsertGroup(r.Context(), g); err != nil {
		writeError(w, http.StatusInternalServerError, "create failed")
		return
	}
	members := gw.memberIDs()
	if len(members) > 0 {
		if err := p.store.ReplaceGroupMembers(r.Context(), idp, g.SCIMID, members); err != nil {
			writeError(w, http.StatusInternalServerError, "member set failed")
			return
		}
	}
	p.audit(r.Context(), idp, "scim.group.provision", g.DisplayName, types.AuditOK)
	writeJSON(w, http.StatusCreated, groupResource(g, members))
}

func (p *Provider) replaceGroup(w http.ResponseWriter, r *http.Request, idp, id string) {
	cur, _, err := p.store.GetGroup(r.Context(), idp, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "group not found")
		return
	}
	body, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body")
		return
	}
	var gw groupWire
	if err := json.Unmarshal(body, &gw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid SCIM group")
		return
	}
	cur.DisplayName = gw.DisplayName
	cur.ExternalID = gw.ExternalID
	if err := p.store.UpsertGroup(r.Context(), cur); err != nil {
		writeError(w, http.StatusInternalServerError, "replace failed")
		return
	}
	members := gw.memberIDs()
	if err := p.store.ReplaceGroupMembers(r.Context(), idp, id, members); err != nil {
		writeError(w, http.StatusInternalServerError, "member set failed")
		return
	}
	p.audit(r.Context(), idp, "scim.group.update", cur.DisplayName, types.AuditOK)
	writeJSON(w, http.StatusOK, groupResource(cur, members))
}

func (p *Provider) patchGroup(w http.ResponseWriter, r *http.Request, idp, id string) {
	g, _, err := p.store.GetGroup(r.Context(), idp, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "group not found")
		return
	}
	body, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body")
		return
	}
	var patch patchWire
	if err := json.Unmarshal(body, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid PatchOp")
		return
	}
	for _, op := range patch.Operations {
		switch strings.ToLower(op.Op) {
		case "add":
			add := op.memberValues()
			if err := p.store.AddGroupMembers(r.Context(), idp, id, add); err != nil {
				writeError(w, http.StatusInternalServerError, "member add failed")
				return
			}
			if len(add) > 0 {
				p.audit(r.Context(), idp, "scim.group.member-add", g.DisplayName, types.AuditOK)
			}
		case "remove":
			rem := op.memberValues()
			if len(rem) == 0 && strings.HasPrefix(strings.ToLower(op.Path), "members") {
				// A bare "remove members" clears the whole set.
				if err := p.store.ReplaceGroupMembers(r.Context(), idp, id, nil); err != nil {
					writeError(w, http.StatusInternalServerError, "member clear failed")
					return
				}
			} else if err := p.store.RemoveGroupMembers(r.Context(), idp, id, rem); err != nil {
				writeError(w, http.StatusInternalServerError, "member remove failed")
				return
			}
			p.audit(r.Context(), idp, "scim.group.member-remove", g.DisplayName, types.AuditOK)
		case "replace":
			if strings.HasPrefix(strings.ToLower(op.Path), "members") || op.Path == "" {
				if err := p.store.ReplaceGroupMembers(r.Context(), idp, id, op.memberValues()); err != nil {
					writeError(w, http.StatusInternalServerError, "member replace failed")
					return
				}
				p.audit(r.Context(), idp, "scim.group.update", g.DisplayName, types.AuditOK)
			}
		}
	}
	got, members, _ := p.store.GetGroup(r.Context(), idp, id)
	writeJSON(w, http.StatusOK, groupResource(got, members))
}

func (p *Provider) deleteGroup(w http.ResponseWriter, r *http.Request, idp, id string) {
	g, _, err := p.store.GetGroup(r.Context(), idp, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "group not found")
		return
	}
	if err := p.store.DeleteGroup(r.Context(), idp, id); err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	p.audit(r.Context(), idp, "scim.group.delete", g.DisplayName, types.AuditOK)
	w.WriteHeader(http.StatusNoContent)
}

// ── audit ─────────────────────────────────────────────────────────────────────

// audit records one provisioning action into the one audit stream (§1.6). The
// actor is the IdP (a service Principal); best-effort, never hides failure (§1.8).
func (p *Provider) audit(ctx context.Context, idp, action, object, outcome string) {
	if err := p.store.RecordAudit(context.WithoutCancel(ctx), types.AuditEvent{
		PrincipalID: "idp:" + idp, PrincipalKind: "service",
		Action: action, Object: object, Outcome: outcome,
	}); err != nil {
		p.log.Error("scim audit record failed", "action", action, "err", err)
	}
}

// auditActive picks provision/deactivate based on an active transition.
func (p *Provider) auditActive(ctx context.Context, idp, principal string, was, now bool, updateAction string) {
	switch {
	case was && !now:
		p.audit(ctx, idp, "scim.user.deactivate", principal, types.AuditOK)
	default:
		p.audit(ctx, idp, updateAction, principal, types.AuditOK)
	}
}

// ── helpers ────────────────────────────────────────────────────────────────────

func readBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, 1<<20))
}

// newID mints a server-assigned SCIM resource id (dependency-free random hex).
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// principalID is the value we expect the OIDC `sub` to carry: SCIM externalId
// when present (the IdP's stable user id, which it also uses as `sub`), else the
// userName. Documented fallback; per-IdP claim config is a follow-up.
func principalID(u userWire) string {
	if u.ExternalID != "" {
		return u.ExternalID
	}
	return u.UserName
}

// parseFilter parses the minimal SCIM filter Okta/Entra use to reconcile:
// `attr eq "value"`. Returns ("","") for anything else (eq-only, hand-rolled).
func parseFilter(f string) (field, value string) {
	f = strings.TrimSpace(f)
	if f == "" {
		return "", ""
	}
	parts := strings.SplitN(f, " ", 3)
	if len(parts) != 3 || !strings.EqualFold(parts[1], "eq") {
		return "", ""
	}
	return parts[0], strings.Trim(parts[2], `"`)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]any{
		"schemas": []string{schemaError},
		"status":  fmt.Sprintf("%d", status),
		"detail":  detail,
	})
}

func listResponse(res []any) map[string]any {
	return map[string]any{
		"schemas":      []string{schemaList},
		"totalResults": len(res),
		"startIndex":   1,
		"itemsPerPage": len(res),
		"Resources":    res,
	}
}

func userResource(i types.SCIMIdentity) map[string]any {
	m := map[string]any{
		"schemas":  []string{schemaUser},
		"id":       i.SCIMID,
		"userName": i.UserName,
		"active":   i.Active,
		"meta":     map[string]any{"resourceType": "User", "location": "/scim/v2/Users/" + i.SCIMID},
	}
	if i.ExternalID != "" {
		m["externalId"] = i.ExternalID
	}
	if len(i.Emails) > 0 {
		m["emails"] = json.RawMessage(i.Emails)
	}
	return m
}

func groupResource(g types.SCIMGroup, members []string) map[string]any {
	ms := make([]map[string]any, 0, len(members))
	for _, m := range members {
		ms = append(ms, map[string]any{"value": m})
	}
	res := map[string]any{
		"schemas":     []string{schemaGroup},
		"id":          g.SCIMID,
		"displayName": g.DisplayName,
		"members":     ms,
		"meta":        map[string]any{"resourceType": "Group", "location": "/scim/v2/Groups/" + g.SCIMID},
	}
	if g.ExternalID != "" {
		res["externalId"] = g.ExternalID
	}
	return res
}
