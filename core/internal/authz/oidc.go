package authz

import (
	"context"
	"fmt"

	oidc "github.com/coreos/go-oidc/v3/oidc"
)

// OIDCResolver verifies Bearer tokens against an OIDC issuer (discovery +
// JWKS) and resolves them to a Principal (charter §2.5: one Principal model
// for humans, services, and agents; the IdP is substrate, not source of
// truth). Verify-only — no login flow lives here; the UI's authorization-code
// flow is a later slice.
type OIDCResolver struct {
	verifier *oidc.IDTokenVerifier
}

// NewOIDCResolver runs issuer discovery (fail fast on misconfiguration).
// audience is optional: when empty, audience checking is skipped — dev
// client_credentials tokens carry client-specific audiences (documented in
// ADR-0009 implementation notes); set STRATT_OIDC_AUDIENCE in production.
func NewOIDCResolver(ctx context.Context, issuer, audience string) (*OIDCResolver, error) {
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("authz: oidc discovery %s: %w", issuer, err)
	}
	cfg := &oidc.Config{ClientID: audience, SkipClientIDCheck: audience == ""}
	return &OIDCResolver{verifier: provider.Verifier(cfg)}, nil
}

// Resolve verifies a raw Bearer token and returns the Principal id and kind.
// The id is the `sub` claim — stable and non-reassignable, unlike usernames
// and emails. Kind is a heuristic for now (profile claims → human, else
// service); an explicit kind claim is a named follow-up in ADR-0009.
func (r *OIDCResolver) Resolve(ctx context.Context, rawToken string) (id, kind string, err error) {
	tok, err := r.verifier.Verify(ctx, rawToken)
	if err != nil {
		return "", "", fmt.Errorf("authz: token verify: %w", err)
	}
	var claims struct {
		PreferredUsername string `json:"preferred_username"`
		Email             string `json:"email"`
	}
	_ = tok.Claims(&claims) // absent profile claims are fine — service token
	kind = KindService
	if claims.PreferredUsername != "" || claims.Email != "" {
		kind = KindHuman
	}
	return tok.Subject, kind, nil
}
