package authz

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// testIssuer is a minimal OIDC issuer: discovery + JWKS + an RSA signer —
// enough for the verify-only resolver without a live IdP.
type testIssuer struct {
	key    *rsa.PrivateKey
	server *httptest.Server
}

func newTestIssuer(t *testing.T) *testIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ti := &testIssuer{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                ti.server.URL,
			"jwks_uri":                              ti.server.URL + "/keys",
			"authorization_endpoint":                ti.server.URL + "/auth",
			"token_endpoint":                        ti.server.URL + "/token",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		pub := key.Public().(*rsa.PublicKey)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{{
				"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "test",
				"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			}},
		})
	})
	ti.server = httptest.NewServer(mux)
	t.Cleanup(ti.server.Close)
	return ti
}

// mint signs an RS256 JWT with the issuer's key over the given claims
// (issuer/expiry defaults applied unless overridden).
func (ti *testIssuer) mint(t *testing.T, claims map[string]any) string {
	t.Helper()
	if _, ok := claims["iss"]; !ok {
		claims["iss"] = ti.server.URL
	}
	if _, ok := claims["exp"]; !ok {
		claims["exp"] = time.Now().Add(time.Hour).Unix()
	}
	claims["iat"] = time.Now().Add(-time.Minute).Unix()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "test"})
	payload, _ := json.Marshal(claims)
	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, ti.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestOIDCResolver(t *testing.T) {
	ti := newTestIssuer(t)
	ctx := context.Background()
	r, err := NewOIDCResolver(ctx, ti.server.URL, "")
	if err != nil {
		t.Fatal(err)
	}

	// Service token: sub only → kind=service.
	id, kind, err := r.Resolve(ctx, ti.mint(t, map[string]any{"sub": "381351939", "aud": "whatever"}))
	if err != nil {
		t.Fatal(err)
	}
	if id != "381351939" || kind != KindService {
		t.Fatalf("got id=%q kind=%q", id, kind)
	}

	// Profile claims → kind=human.
	_, kind, err = r.Resolve(ctx, ti.mint(t, map[string]any{"sub": "42", "aud": "x", "preferred_username": "dstout"}))
	if err != nil {
		t.Fatal(err)
	}
	if kind != KindHuman {
		t.Fatalf("profile claims must resolve human, got %q", kind)
	}

	// Expired, garbage, and wrong-issuer tokens are rejected.
	if _, _, err := r.Resolve(ctx, ti.mint(t, map[string]any{"sub": "42", "aud": "x", "exp": time.Now().Add(-time.Hour).Unix()})); err == nil {
		t.Fatal("expired token must fail")
	}
	if _, _, err := r.Resolve(ctx, "not.a.jwt"); err == nil {
		t.Fatal("garbage must fail")
	}
	if _, _, err := r.Resolve(ctx, ti.mint(t, map[string]any{"sub": "42", "aud": "x", "iss": "https://evil.example"})); err == nil {
		t.Fatal("wrong issuer must fail")
	}

	// Audience enforcement when configured.
	ra, err := NewOIDCResolver(ctx, ti.server.URL, "stratt")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ra.Resolve(ctx, ti.mint(t, map[string]any{"sub": "42", "aud": "other"})); err == nil {
		t.Fatal("audience mismatch must fail")
	}
	if _, _, err := ra.Resolve(ctx, ti.mint(t, map[string]any{"sub": "42", "aud": "stratt"})); err != nil {
		t.Fatalf("matching audience must pass: %v", err)
	}
}

// TestMultiIssuerResolver proves the ADR-0101 multi-issuer invariants — above all I-1:
// an issuer CANNOT assert a Principal in another issuer's namespace (the cross-issuer
// privilege-escalation guard), and an unconfigured issuer is denied (I-5).
func TestMultiIssuerResolver(t *testing.T) {
	a := newTestIssuer(t)
	b := newTestIssuer(t)
	ctx := context.Background()
	r, err := NewMultiIssuerResolver(ctx, []IssuerConfig{
		{Issuer: a.server.URL, Audience: "stratt", SubNamespace: "openbao/", Alias: "openbao"},
		{Issuer: b.server.URL, Audience: "stratt", SubNamespace: "zitadel/", Alias: "zitadel"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Each issuer's Principal id is its namespace PREPENDED to the raw sub (which, for a
	// real OpenBao entity, is a UUID — here a bare string stands in). The namespace comes
	// from the VERIFYING issuer, never from the token.
	if id, kind, err := r.Resolve(ctx, a.mint(t, map[string]any{"sub": "svc-syncer", "aud": "stratt"})); err != nil || id != "openbao/svc-syncer" || kind != KindService {
		t.Fatalf("openbao issuer: id=%q kind=%q err=%v", id, kind, err)
	}
	if id, kind, err := r.Resolve(ctx, b.mint(t, map[string]any{"sub": "alice", "aud": "stratt", "email": "alice@x"})); err != nil || id != "zitadel/alice" || kind != KindHuman {
		t.Fatalf("zitadel issuer: id=%q kind=%q err=%v", id, kind, err)
	}
	// I-1 ESCALATION GUARD (the load-bearing security test): issuer B mints a token whose
	// `sub` is BYTE-IDENTICAL to the one issuer A uses ("svc-syncer"). B's verifier
	// validates it (B's key), but the resolver scopes it under B's namespace — so it
	// resolves to "zitadel/svc-syncer", NEVER "openbao/svc-syncer". B can therefore never
	// forge a Principal that issuer A's tuples grant.
	if id, _, err := r.Resolve(ctx, b.mint(t, map[string]any{"sub": "svc-syncer", "aud": "stratt"})); err != nil || id == "openbao/svc-syncer" {
		t.Fatalf("I-1 VIOLATION: issuer B produced id=%q (must be zitadel-namespaced, never openbao/svc-syncer) err=%v", id, err)
	} else if id != "zitadel/svc-syncer" {
		t.Fatalf("I-1: expected B's sub scoped under its own namespace, got %q", id)
	}
	// I-5: a token from an issuer NOT in the trust set is denied (no verifier passes).
	c := newTestIssuer(t)
	if _, _, err := r.Resolve(ctx, c.mint(t, map[string]any{"sub": "openbao:x", "aud": "stratt"})); err == nil {
		t.Fatal("I-5: a token from an unconfigured issuer must be denied")
	}
}

// TestIssuerConfigParsesHelmJSON locks the helm↔Go contract: the STRATT_OIDC_ISSUERS
// value the chart renders (values-authz.yaml → deployment.yaml `toJson`) must unmarshal
// into IssuerConfig. Field names are lowercase-first (issuer/audience/subNamespace/alias);
// Go's case-insensitive match binds them to the struct.
func TestIssuerConfigParsesHelmJSON(t *testing.T) {
	// Byte-for-byte the chart's rendered value (helm template … values-authz.yaml).
	raw := `[{"alias":"openbao","audience":"stratt","issuer":"http://openbao.stratt-dev.svc:8200/v1/identity/oidc","subNamespace":"openbao/"}]`
	var cfgs []IssuerConfig
	if err := json.Unmarshal([]byte(raw), &cfgs); err != nil {
		t.Fatalf("the rendered STRATT_OIDC_ISSUERS must parse into IssuerConfig: %v", err)
	}
	if len(cfgs) != 1 {
		t.Fatalf("got %d issuers", len(cfgs))
	}
	c := cfgs[0]
	if c.Issuer == "" || c.Audience != "stratt" || c.SubNamespace != "openbao/" || c.Alias != "openbao" {
		t.Fatalf("field binding wrong: %+v", c)
	}
}

// TestMultiIssuerDisjointRequired proves the I-1 boot-time disjointness validation.
func TestMultiIssuerDisjointRequired(t *testing.T) {
	a := newTestIssuer(t)
	b := newTestIssuer(t)
	ctx := context.Background()
	// Overlapping namespaces ("svc/" is a prefix of "svc/sub/") — construction fails.
	if _, err := NewMultiIssuerResolver(ctx, []IssuerConfig{
		{Issuer: a.server.URL, SubNamespace: "svc/", Audience: "x"},
		{Issuer: b.server.URL, SubNamespace: "svc/sub/", Audience: "x"},
	}); err == nil {
		t.Fatal("overlapping sub namespaces must be rejected at construction (I-1)")
	}
	// Empty namespace with multiple issuers — construction fails.
	if _, err := NewMultiIssuerResolver(ctx, []IssuerConfig{
		{Issuer: a.server.URL, SubNamespace: "a/", Audience: "x"},
		{Issuer: b.server.URL, SubNamespace: "", Audience: "x"},
	}); err == nil {
		t.Fatal("empty subNamespace with multiple issuers must be rejected (I-1)")
	}
	// A duplicate issuer URL is an ambiguous trust map — rejected at construction
	// (guardian flag 2: else the first-match loop silently shadows the second entry).
	if _, err := NewMultiIssuerResolver(ctx, []IssuerConfig{
		{Issuer: a.server.URL, SubNamespace: "x/", Audience: "s"},
		{Issuer: a.server.URL, SubNamespace: "y/", Audience: "s"},
	}); err == nil {
		t.Fatal("a duplicate issuer URL must be rejected at construction")
	}
	// A ':' in the namespace would make the Principal an unparseable OpenFGA subject id
	// (`principal:<ns><sub>`, where ':' is the reserved type separator) — rejected at boot
	// so every authz check can't silently 400 at runtime. Applies to a lone issuer too.
	if _, err := NewMultiIssuerResolver(ctx, []IssuerConfig{
		{Issuer: a.server.URL, SubNamespace: "openbao:", Audience: "x"},
	}); err == nil {
		t.Fatal("a subNamespace containing ':' must be rejected (authz-subject-unsafe)")
	}
}
