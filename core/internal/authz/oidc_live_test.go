package authz

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestLiveOpenBaoWorkloadIdentity proves the ADR-0101 Phase B chain end-to-end against a
// REAL OpenBao identity/oidc provider (no mock): a workload logs in (userpass here stands in
// for the in-cluster K8s-auth step, I-6), OpenBao mints a signed OIDC ID token, and the
// production multi-issuer OIDCResolver verifies it via discovery+JWKS and resolves it to the
// issuer-scoped Principal `openbao/<entity-uuid>` (I-1), KindService (I-3 heuristic). It also
// proves a garbage Bearer is denied (I-5 fail-closed).
//
// Gated on STRATT_LIVE_OPENBAO_ADDR (e.g. http://localhost:8200); run deploy/dev/openbao-
// bootstrap.sh first so the key/role/entity/userpass exist.
func TestLiveOpenBaoWorkloadIdentity(t *testing.T) {
	addr := os.Getenv("STRATT_LIVE_OPENBAO_ADDR")
	if addr == "" {
		t.Skip("set STRATT_LIVE_OPENBAO_ADDR to run the live OpenBao-OIDC workload-identity proof")
	}
	root := os.Getenv("STRATT_LIVE_OPENBAO_TOKEN")
	if root == "" {
		root = "stratt-dev-root"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The issuer strattd must trust — read it from OpenBao's own discovery so the proof
	// uses whatever the running server advertises (never a hardcoded host).
	var disc struct {
		Issuer string `json:"issuer"`
	}
	baoGet(t, ctx, addr+"/v1/identity/oidc/.well-known/openid-configuration", "", &disc)
	if disc.Issuer == "" {
		t.Fatal("identity/oidc discovery advertised no issuer — run openbao-bootstrap.sh")
	}
	t.Logf("LIVE issuer: %s", disc.Issuer)

	// The entity UUID is the sub → the expected Principal is openbao/<uuid>.
	var ent struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	baoGet(t, ctx, addr+"/v1/identity/entity/name/svc-syncer", root, &ent)
	if ent.Data.ID == "" {
		t.Fatal("entity svc-syncer not found — run openbao-bootstrap.sh")
	}
	want := "openbao/" + ent.Data.ID

	// Workload login (dev stand-in for K8s auth). Then mint the OIDC ID token as the entity.
	var login struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	baoPost(t, ctx, addr+"/v1/auth/userpass/login/svc-syncer", "", `{"password":"devpw"}`, &login)
	if login.Auth.ClientToken == "" {
		t.Fatal("userpass login failed — run openbao-bootstrap.sh")
	}
	var minted struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	baoGet(t, ctx, addr+"/v1/identity/oidc/token/stratt", login.Auth.ClientToken, &minted)
	if minted.Data.Token == "" {
		t.Fatal("identity/oidc token mint failed — check the stratt-oidc-mint policy")
	}

	// The PRODUCTION resolver, configured exactly as the values-authz profile would.
	r, err := NewMultiIssuerResolver(ctx, []IssuerConfig{
		{Issuer: disc.Issuer, Audience: "stratt", SubNamespace: "openbao/", Alias: "openbao"},
	})
	if err != nil {
		t.Fatalf("resolver init against live OpenBao: %v", err)
	}

	id, kind, err := r.Resolve(ctx, minted.Data.Token)
	if err != nil {
		t.Fatalf("live resolve of a real OpenBao token: %v", err)
	}
	if id != want {
		t.Fatalf("Principal id: got %q want %q (issuer-scoped uuid, I-1)", id, want)
	}
	if kind != KindService {
		t.Fatalf("a workload token (no email/preferred_username) must resolve KindService, got %q", kind)
	}
	t.Logf("LIVE workload identity: real OpenBao token -> Principal %q (%s) via discovery+JWKS", id, kind)

	// I-5 fail-closed: a garbage Bearer is denied, never resolved.
	if _, _, err := r.Resolve(ctx, "not.a.jwt"); err == nil {
		t.Fatal("I-5: a malformed token must be denied")
	}
}

// TestLivePhaseAAuthzChain is the ADR-0101 Phase A end-to-end proof against BOTH real
// backends at once (no mock, no dev header): a real OpenBao-minted Bearer resolves to a
// Principal, and a real OpenFGA server makes the authorization decision. It proves the
// three deny-by-default properties Phase A promises: (1) an unauthenticated request has no
// Principal; (2) a real Principal WITHOUT a granting tuple is denied; (3) the SAME Principal
// WITH the tuple passes — and revoking the manifest denies again.
//
// Gated on STRATT_LIVE_OPENBAO_ADDR + a reachable OpenFGA (STRATT_OPENFGA_URL or
// localhost:8081, both from `task dev:up`). This is the compose-backend proof of the chain
// the values-authz.yaml profile packages for in-cluster (the kind e2e is the ADR follow-up).
func TestLivePhaseAAuthzChain(t *testing.T) {
	addr := os.Getenv("STRATT_LIVE_OPENBAO_ADDR")
	if addr == "" {
		t.Skip("set STRATT_LIVE_OPENBAO_ADDR to run the Phase A real-authz chain proof")
	}
	fgaURL := openFGAURL(t) // skips if OpenFGA unreachable
	root := os.Getenv("STRATT_LIVE_OPENBAO_TOKEN")
	if root == "" {
		root = "stratt-dev-root"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// --- Identity: a real OpenBao Bearer → Principal (as Phase B proved) ---
	var disc struct {
		Issuer string `json:"issuer"`
	}
	baoGet(t, ctx, addr+"/v1/identity/oidc/.well-known/openid-configuration", "", &disc)
	var login struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	baoPost(t, ctx, addr+"/v1/auth/userpass/login/svc-syncer", "", `{"password":"devpw"}`, &login)
	var minted struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	baoGet(t, ctx, addr+"/v1/identity/oidc/token/stratt", login.Auth.ClientToken, &minted)
	if disc.Issuer == "" || login.Auth.ClientToken == "" || minted.Data.Token == "" {
		t.Fatal("OpenBao not bootstrapped — run deploy/dev/openbao-bootstrap.sh")
	}
	resolver, err := NewMultiIssuerResolver(ctx, []IssuerConfig{
		{Issuer: disc.Issuer, Audience: "stratt", SubNamespace: "openbao/", Alias: "openbao"},
	})
	if err != nil {
		t.Fatalf("resolver init: %v", err)
	}

	// Property 1: an unauthenticated request has no Principal (no Bearer to resolve).
	if _, _, err := resolver.Resolve(ctx, ""); err == nil {
		t.Fatal("anonymous (empty Bearer) must not resolve to any Principal")
	}
	principal, _, err := resolver.Resolve(ctx, minted.Data.Token)
	if err != nil {
		t.Fatalf("resolve real Bearer: %v", err)
	}
	t.Logf("LIVE Principal from real OpenBao Bearer: %q", principal)

	// --- Decision: a real OpenFGA server, deny-by-default ---
	fga, err := NewOpenFGAAuthorizer(ctx, fgaURL)
	if err != nil {
		t.Fatalf("openfga authorizer: %v", err)
	}
	const view = "view:authz-proof"

	// Property 2: no granting tuple ⇒ the real Principal is DENIED runner on the View.
	if err := fga.SyncTuples(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if ok, err := fga.Check(ctx, principal, RelationRunner, view); err != nil || ok {
		t.Fatalf("deny-by-default: ungranted Principal must NOT be runner (ok=%v err=%v)", ok, err)
	}

	// Property 3: grant the SAME Principal via the tuple manifest ⇒ it now passes;
	// then emptying the manifest revokes (Sync is authoritative, not additive).
	grant := []Tuple{{User: "principal:" + principal, Relation: RelationRunner, Object: view}}
	if err := fga.SyncTuples(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if ok, err := fga.Check(ctx, principal, RelationRunner, view); err != nil || !ok {
		t.Fatalf("granted Principal must be runner on the View (ok=%v err=%v)", ok, err)
	}
	t.Logf("LIVE authz: %q granted runner on %s via real OpenFGA", principal, view)
	if err := fga.SyncTuples(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if ok, _ := fga.Check(ctx, principal, RelationRunner, view); ok {
		t.Fatal("revoke: emptying the manifest must deny again")
	}
	t.Log("LIVE Phase A chain proven: real Bearer -> Principal -> real OpenFGA deny/allow/revoke")
}

func baoGet(t *testing.T, ctx context.Context, url, token string, out any) {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	baoDo(t, req, out)
}

func baoPost(t *testing.T, ctx context.Context, url, token, body string, out any) {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBufferString(body))
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	baoDo(t, req, out)
}

func baoDo(t *testing.T, req *http.Request, out any) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		t.Fatalf("%s %s: status %d: %s", req.Method, req.URL.Path, resp.StatusCode, b)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("%s %s: decode: %v (body=%s)", req.Method, req.URL.Path, err, b)
	}
}
