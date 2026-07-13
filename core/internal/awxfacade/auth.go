package awxfacade

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"github.com/dstout-devops/stratt/core/internal/authz"
)

var (
	errNoOIDC   = errors.New("awxfacade: OIDC not configured")
	errBadBasic = errors.New("awxfacade: malformed Basic credential")
)

// principal returns the request's resolved Principal (id, kind), ok=false when
// anonymous.
func principal(r *http.Request) (id, kind string, ok bool) {
	return authz.PrincipalFrom(r.Context())
}

// basicPassword extracts the password from an `Authorization: Basic` header —
// which the façade treats as the bearer token (the username is informational).
func basicPassword(authHeader string) (string, bool) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(authHeader, "Basic "))
	if err != nil {
		return "", false
	}
	_, pass, ok := strings.Cut(string(raw), ":")
	return pass, ok
}

// resolve maps an AWX request's auth to a Stratt Principal, reusing the native
// identity seam — NO new token/password store (§2.5/§1.6). Order:
//  1. Authorization: Bearer <t>  → OIDC.Resolve(t)         (same as /api/v1)
//  2. Authorization: Basic <b64> → OIDC.Resolve(password)  (the collections'
//     default; the password is verified AS a JWT, never stored/compared —
//     username is informational). This is what lets `ansible.controller`
//     playbooks that send user/pass authenticate without a password store.
//  3. dev X-Stratt-Principal header (when enabled).
//  4. else anonymous ("").
func (f *Facade) resolve(ctx context.Context, h http.Header) (id, kind string, err error) {
	authHeader := h.Get("Authorization")
	switch {
	case strings.HasPrefix(authHeader, "Bearer "):
		if f.cfg.OIDC == nil {
			return "", "", errNoOIDC
		}
		return f.cfg.OIDC.Resolve(ctx, strings.TrimPrefix(authHeader, "Bearer "))
	case strings.HasPrefix(authHeader, "Basic "):
		if f.cfg.OIDC == nil {
			return "", "", errNoOIDC
		}
		pass, ok := basicPassword(authHeader)
		if !ok {
			return "", "", errBadBasic
		}
		return f.cfg.OIDC.Resolve(ctx, pass)
	}
	if f.cfg.DevPrincipalHeader {
		if p := h.Get("X-Stratt-Principal"); p != "" {
			k := h.Get("X-Stratt-Principal-Kind")
			if k == "" {
				k = authz.KindHuman
			}
			return p, k, nil
		}
	}
	return "", "", nil
}

// requireRunner enforces View-scoped execution authz (§2.5, ADR-0028) on the
// façade, symmetric with the native path — the compat surface is never a weaker
// authz path (§1.6). Returns false (and writes an AWX-shaped 403) when the
// principal lacks `runner` on the view.
func (f *Facade) requireRunner(ctx context.Context, w http.ResponseWriter, principal, view string) bool {
	if principal == "" || f.cfg.Authz == nil {
		awxErr(w, http.StatusForbidden, "You do not have permission to perform this action.")
		return false
	}
	allowed, err := f.cfg.Authz.Check(ctx, principal, authz.RelationRunner, "view:"+view)
	if err != nil {
		awxErr(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if !allowed {
		awxErr(w, http.StatusForbidden, "You do not have permission to perform this action.")
		return false
	}
	return true
}

// authed wraps a handler so it resolves + stamps the Principal on the request
// context (via authz.WithPrincipal), exactly as /api/v1's principalMiddleware
// does — so requireGrant-style checks and the launch Principal work unchanged.
// An invalid credential is a hard 401 (never a downgrade to anonymous).
func (f *Facade) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, kind, err := f.resolve(r.Context(), r.Header)
		if err != nil {
			awxErr(w, http.StatusUnauthorized, "Invalid token.")
			return
		}
		if id != "" {
			r = r.WithContext(authz.WithPrincipal(r.Context(), id, kind))
		}
		next(w, r)
	}
}
