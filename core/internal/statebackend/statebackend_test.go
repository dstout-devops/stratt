package statebackend

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/keycustodian"
)

const testKey = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func testStore(t *testing.T) *graph.Store {
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
	name := fmt.Sprintf("stratt_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
		admin.Close()
	})
	u, err := neturl.Parse(url)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	store, err := graph.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("connect+migrate test database: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

func TestCryptoRoundTrip(t *testing.T) {
	b, err := New(testKey, nil, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	enc, err := b.encrypt(context.Background(), []byte(`{"version":4}`))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(enc, []byte("version")) {
		t.Fatal("ciphertext must not contain plaintext")
	}
	plain, err := b.decrypt(context.Background(), enc)
	if err != nil || string(plain) != `{"version":4}` {
		t.Fatalf("round trip: %s %v", plain, err)
	}
	// A different key cannot decrypt.
	b2, _ := New(strings.Repeat("ff", 32), nil, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if _, err := b2.decrypt(context.Background(), enc); err == nil {
		t.Fatal("wrong key must fail to decrypt")
	}
	// Bad key shapes are refused.
	if _, err := New("shortkey", nil, slog.Default()); err == nil {
		t.Fatal("short key must be refused")
	}
}

// TestLegacyBlobStillDecrypts is the load-bearing migration guarantee (ADR-0100): state
// written by the pre-envelope code (bare AES-GCM under the state key) must still decrypt
// through the legacy fallback — a rebuilt/upgraded backend never loses existing state.
func TestLegacyBlobStillDecrypts(t *testing.T) {
	b, _ := New(testKey, nil, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	// Reproduce exactly what the old encrypt() produced: nonce || AES-256-GCM(stateKey).
	key, _ := hex.DecodeString(testKey)
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	nonce := make([]byte, aead.NonceSize())
	legacy := append(nonce, aead.Seal(nil, nonce, []byte(`{"legacy":true}`), nil)...)

	plain, err := b.decrypt(context.Background(), legacy)
	if err != nil || string(plain) != `{"legacy":true}` {
		t.Fatalf("legacy bare-AES blob must decrypt via fallback: %s %v", plain, err)
	}
	// And a fresh write is an envelope (the migration re-seals on next write).
	enc, _ := b.encrypt(context.Background(), []byte(`{"new":true}`))
	if !keycustodian.IsEnveloped(enc) {
		t.Fatal("new writes must be envelope-encrypted")
	}
}

func TestBackendProtocol(t *testing.T) {
	store := testStore(t)
	b, err := New(testKey, store, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.StripPrefix("", b.Handler()))
	defer srv.Close()

	ws := "test-ws"
	cred := b.WorkspaceCredential(ws)
	do := func(method, id string, body string, auth string) *http.Response {
		req, _ := http.NewRequest(method, srv.URL+"/statebackend/"+ws+id, strings.NewReader(body))
		if auth != "" {
			req.SetBasicAuth("stratt", auth)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	// Auth: no/wrong credential → 401; per-workspace scoping.
	if res := do("GET", "", "", ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no auth: %d", res.StatusCode)
	}
	if res := do("GET", "", "", b.WorkspaceCredential("other-ws")); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cross-workspace credential must fail: %d", res.StatusCode)
	}

	// Empty state → 404.
	if res := do("GET", "", "", cred); res.StatusCode != http.StatusNotFound {
		t.Fatalf("empty state: %d", res.StatusCode)
	}

	// LOCK → OK; second LOCK → 423 with holder doc.
	if res := do("LOCK", "", `{"ID":"lock-1","Who":"tester"}`, cred); res.StatusCode != http.StatusOK {
		t.Fatalf("lock: %d", res.StatusCode)
	}
	res := do("LOCK", "", `{"ID":"lock-2"}`, cred)
	if res.StatusCode != http.StatusLocked {
		t.Fatalf("second lock: %d", res.StatusCode)
	}
	holder, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(holder), "lock-1") {
		t.Fatalf("423 must return the holder doc: %s", holder)
	}

	// POST without the lock ID → 423; with it → 200.
	if res := do("POST", "?ID=wrong", `{"version":4,"serial":1}`, cred); res.StatusCode != http.StatusLocked {
		t.Fatalf("mismatched lock id post: %d", res.StatusCode)
	}
	if res := do("POST", "?ID=lock-1", `{"version":4,"serial":1}`, cred); res.StatusCode != http.StatusOK {
		t.Fatalf("post: %d", res.StatusCode)
	}

	// GET returns the plaintext; the stored row is ciphertext.
	res = do("GET", "", "", cred)
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), `"serial":1`) {
		t.Fatalf("get: %d %s", res.StatusCode, body)
	}
	raw, _, err := store.GetOpenTofuState(context.Background(), ws)
	if err != nil || bytes.Contains(raw, []byte("serial")) {
		t.Fatalf("state at rest must be ciphertext: %v %v", err, bytes.Contains(raw, []byte("serial")))
	}

	// UNLOCK → OK; a fresh LOCK succeeds.
	if res := do("UNLOCK", "", `{"ID":"lock-1"}`, cred); res.StatusCode != http.StatusOK {
		t.Fatalf("unlock: %d", res.StatusCode)
	}
	if res := do("LOCK", "", `{"ID":"lock-3"}`, cred); res.StatusCode != http.StatusOK {
		t.Fatalf("relock: %d", res.StatusCode)
	}
}
