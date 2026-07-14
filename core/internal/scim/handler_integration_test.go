package scim

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

// throwawayStore mirrors graph.testStore (unexported there): a fresh migrated DB
// so the SCIM HTTP path runs against real storage. Skips when no DB is reachable.
func throwawayStore(t *testing.T) *graph.Store {
	t.Helper()
	url := os.Getenv("STRATT_TEST_DATABASE_URL")
	if url == "" {
		url = "postgres://stratt:stratt-dev@localhost:5432/stratt"
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("no test database reachable (%v) — run `task dev:up`", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Skipf("no test database reachable (%v) — run `task dev:up`", err)
	}
	name := fmt.Sprintf("stratt_scim_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
		admin.Close()
	})
	// Rebuild the URL onto the throwaway database.
	dbURL := url
	if i := lastSlash(url); i >= 0 {
		dbURL = url[:i+1] + name
	}
	store, err := graph.Connect(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect throwaway: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

// TestSCIMHandler drives the SCIM 2.0 HTTP surface: token auth, Users/Groups
// CRUD, filter, and PATCH deactivation — the full push path an IdP exercises.
func TestSCIMHandler(t *testing.T) {
	store := throwawayStore(t)
	ctx := context.Background()

	const rawToken = "s3cr3t-idp-token"
	sum := sha256.Sum256([]byte(rawToken))
	if err := store.UpsertIDP(ctx, types.SCIMIdP{Name: "okta", TokenHash: hex.EncodeToString(sum[:])}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(New(store, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	t.Cleanup(srv.Close)

	do := func(method, path, token, body string) (*http.Response, []byte) {
		req, _ := http.NewRequest(method, srv.URL+"/scim/v2"+path, bytes.NewReader([]byte(body)))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp, out
	}

	// Auth: no token and wrong token both 401; discovery works with the token.
	if resp, _ := do("GET", "/ServiceProviderConfig", "", ""); resp.StatusCode != 401 {
		t.Fatalf("no token: got %d want 401", resp.StatusCode)
	}
	if resp, _ := do("GET", "/ServiceProviderConfig", "wrong", ""); resp.StatusCode != 401 {
		t.Fatalf("wrong token: got %d want 401", resp.StatusCode)
	}
	if resp, _ := do("GET", "/ServiceProviderConfig", rawToken, ""); resp.StatusCode != 200 {
		t.Fatalf("discovery: got %d want 200", resp.StatusCode)
	}

	// Provision a user.
	resp, out := do("POST", "/Users", rawToken, `{"userName":"alice@corp","externalId":"sub-alice","active":true}`)
	if resp.StatusCode != 201 {
		t.Fatalf("create user: %d %s", resp.StatusCode, out)
	}
	var created map[string]any
	if err := json.Unmarshal(out, &created); err != nil {
		t.Fatal(err)
	}
	uid, _ := created["id"].(string)
	if uid == "" {
		t.Fatalf("no id assigned: %s", out)
	}

	// Filter reconcile (query params URL-encoded, as an IdP would send).
	_, out = do("GET", `/Users?filter=userName%20eq%20%22alice@corp%22`, rawToken, "")
	var list map[string]any
	if err := json.Unmarshal(out, &list); err != nil {
		t.Fatalf("filter response not JSON: %s", out)
	}
	if total, _ := list["totalResults"].(float64); total != 1 {
		t.Fatalf("filter: want 1 result, got %s", out)
	}

	// Deactivate via PATCH → active:false.
	resp, out = do("PATCH", "/Users/"+uid, rawToken, `{"Operations":[{"op":"replace","path":"active","value":false}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("patch: %d %s", resp.StatusCode, out)
	}
	var patched map[string]any
	_ = json.Unmarshal(out, &patched)
	if patched["active"].(bool) {
		t.Fatalf("patch should deactivate: %s", out)
	}

	// Group with the user as member.
	resp, out = do("POST", "/Groups", rawToken, fmt.Sprintf(`{"displayName":"Platform Eng","members":[{"value":%q}]}`, uid))
	if resp.StatusCode != 201 {
		t.Fatalf("create group: %d %s", resp.StatusCode, out)
	}

	// Delete the user → 204.
	if resp, _ := do("DELETE", "/Users/"+uid, rawToken, ""); resp.StatusCode != 204 {
		t.Fatalf("delete: got %d want 204", resp.StatusCode)
	}

	// The provisioning actions landed in the one audit stream (§1.6).
	events, err := store.ListAudit(ctx, "idp:okta", "", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("expected SCIM provisioning events in the audit stream")
	}
}
