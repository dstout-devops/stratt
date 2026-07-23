package authz

import (
	"context"
	"fmt"
	"strings"

	oidc "github.com/coreos/go-oidc/v3/oidc"
)

// IssuerConfig declares one trusted OIDC issuer (ADR-0101). SubNamespace is the DISJOINT
// prefix the resolver PREPENDS to a verified token's `sub` to form the issuer-scoped
// Principal id (I-1), e.g. "openbao/" → Principal "openbao/<uuid>". It must not contain
// ':' (the Principal is used as an OpenFGA authz subject id, where ':' is reserved —
// boot-checked). The issuer component is contributed by the issuer that
// CRYPTOGRAPHICALLY VERIFIED the token — never trusted from a token claim — so a `sub`
// minted under issuer B can never resolve to issuer A's Principal (no cross-issuer
// collision → privilege escalation). The namespaces must be pairwise disjoint (boot-
// checked) so the resulting Principal ids stay globally unambiguous. Empty SubNamespace
// is allowed ONLY for a single-issuer deployment (one namespace, no collision surface;
// the Principal id is then the bare `sub`, preserving back-compat).
//
// Note (empirical, OpenBao 2.5.5): an `identity/oidc` token's `sub` is the entity's stable
// UUID, not its name — a non-reassignable identifier, so the (namespace+sub) Principal
// satisfies I-4 for free (a recreated entity gets a new UUID and does NOT inherit grants).
// Readable name-based Principals via a role claim template are a documented follow-up and
// would reintroduce the name-immutability requirement I-4 guards.
type IssuerConfig struct {
	Issuer       string
	Audience     string
	SubNamespace string
	Alias        string // short, stable label for logs/errors (never load-bearing)
}

// issuerTrust is a configured issuer + its verifier + its allowed sub namespace.
type issuerTrust struct {
	cfg      IssuerConfig
	verifier *oidc.IDTokenVerifier
}

// OIDCResolver verifies Bearer tokens against one or MORE OIDC issuers (discovery +
// JWKS) and resolves them to a Principal (charter §1.6: one Principal model for humans,
// services, and agents; the IdP is substrate, not source of truth). Verify-only — no
// login flow lives here. Multiple issuers let per-Cell OpenBao (workloads/sovereignty)
// and an optional central Zitadel (human SSO) coexist under the one Principal model
// (ADR-0101 §1.5). Fail-closed throughout (I-5).
type OIDCResolver struct {
	issuers []issuerTrust
}

// NewOIDCResolver builds a SINGLE-issuer resolver (back-compat). audience empty ⇒
// audience check skipped (dev only — production sets it; the boot guard enforces I-2).
func NewOIDCResolver(ctx context.Context, issuer, audience string) (*OIDCResolver, error) {
	return NewMultiIssuerResolver(ctx, []IssuerConfig{{Issuer: issuer, Audience: audience, Alias: issuer}})
}

// NewMultiIssuerResolver builds a resolver over one or more issuers (ADR-0101). It is
// FAIL-CLOSED at init (I-5): every issuer's discovery must succeed, or construction
// fails — trust is never silently narrowed. It also boot-validates that the sub
// namespaces are pairwise DISJOINT (I-1), so a sub can be attributed to exactly one
// issuer; overlapping namespaces are a hard error, never a silent overlap.
func NewMultiIssuerResolver(ctx context.Context, cfgs []IssuerConfig) (*OIDCResolver, error) {
	if len(cfgs) == 0 {
		return nil, fmt.Errorf("authz: no OIDC issuers configured")
	}
	// Fail fast if a namespace would produce an authz-unsafe Principal id. The Principal
	// (SubNamespace + sub) is used verbatim as the authz subject — an OpenFGA user id of
	// the form `principal:<id>`, where `:` is the reserved type separator. A ':' in the
	// namespace silently makes EVERY authz check malformed at runtime; reject it at boot.
	for _, c := range cfgs {
		if strings.Contains(c.SubNamespace, ":") {
			return nil, fmt.Errorf("authz: issuer %q subNamespace %q must not contain ':' — the Principal id is an authz subject id and ':' is reserved (use e.g. %q)", alias(c), c.SubNamespace, strings.ReplaceAll(c.SubNamespace, ":", "/"))
		}
	}
	// Reject a duplicate issuer URL: two entries sharing one issuer would make the
	// first-match resolve loop silently pick one namespace and shadow the other — an
	// ambiguous trust map the config must not be able to express (guardian flag 2).
	seen := map[string]bool{}
	for _, c := range cfgs {
		if seen[c.Issuer] {
			return nil, fmt.Errorf("authz: duplicate OIDC issuer %q — each issuer URL may appear once", c.Issuer)
		}
		seen[c.Issuer] = true
	}
	// I-1: reject overlapping sub namespaces. Empty is allowed only for a lone issuer.
	if len(cfgs) > 1 {
		for i := range cfgs {
			if cfgs[i].SubNamespace == "" {
				return nil, fmt.Errorf("authz: issuer %q needs a non-empty subNamespace when multiple issuers are configured (I-1)", alias(cfgs[i]))
			}
			for j := range cfgs {
				if i != j && namespacesOverlap(cfgs[i].SubNamespace, cfgs[j].SubNamespace) {
					return nil, fmt.Errorf("authz: issuer sub namespaces %q and %q overlap — must be disjoint (I-1)", cfgs[i].SubNamespace, cfgs[j].SubNamespace)
				}
			}
		}
	}
	var trusts []issuerTrust
	for _, c := range cfgs {
		provider, err := oidc.NewProvider(ctx, c.Issuer)
		if err != nil {
			return nil, fmt.Errorf("authz: oidc discovery %s: %w", c.Issuer, err)
		}
		verifier := provider.Verifier(&oidc.Config{ClientID: c.Audience, SkipClientIDCheck: c.Audience == ""})
		trusts = append(trusts, issuerTrust{cfg: c, verifier: verifier})
	}
	return &OIDCResolver{issuers: trusts}, nil
}

// Resolve verifies a raw Bearer token against the configured issuers and returns the
// Principal id + kind. FAIL-CLOSED (I-5): a token is accepted only if a configured
// issuer's verifier passes its signature+iss+aud checks; if NONE verifies, it is denied.
// The scoping issuer is the one that CRYPTOGRAPHICALLY VERIFIED the token (never a
// pre-verification `iss` claim). I-1: the Principal id is `verifying-issuer-namespace +
// sub` — the namespace comes from the verifier, so a `sub` minted under one issuer can
// never resolve into another issuer's namespace. Disjoint namespaces (boot-checked) keep
// the id globally unambiguous.
func (r *OIDCResolver) Resolve(ctx context.Context, rawToken string) (id, kind string, err error) {
	for _, it := range r.issuers {
		tok, verr := it.verifier.Verify(ctx, rawToken)
		if verr != nil {
			continue // this issuer did not verify it — try the next
		}
		// Verified by it.cfg. I-1: scope the Principal to the VERIFYING issuer by
		// prepending its namespace — never trust an issuer/namespace claimed by the token.
		id = it.cfg.SubNamespace + tok.Subject
		var claims struct {
			PreferredUsername string `json:"preferred_username"`
			Email             string `json:"email"`
		}
		_ = tok.Claims(&claims)
		// kind is a heuristic and is NEVER load-bearing in an authz decision (I-3);
		// deny-by-default keys on the Principal id + tuples. An explicit kind claim
		// (incl. KindAgent) is a named ADR-0009/0101 follow-up.
		kind = KindService
		if claims.PreferredUsername != "" || claims.Email != "" {
			kind = KindHuman
		}
		return id, kind, nil
	}
	return "", "", fmt.Errorf("authz: no configured issuer verified the token")
}

// namespacesOverlap reports whether two sub-namespace prefixes could ever both match one
// sub (i.e. one is a prefix of the other) — the I-1 disjointness check.
func namespacesOverlap(a, b string) bool {
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

func alias(c IssuerConfig) string {
	if c.Alias != "" {
		return c.Alias
	}
	return c.Issuer
}
