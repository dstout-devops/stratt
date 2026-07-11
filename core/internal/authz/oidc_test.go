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
