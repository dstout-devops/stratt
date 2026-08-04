package emitters

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// ── ADR-0164 D1 · the header is the source's to choose ────────────────────────────────────────

const demoToken = "s3cret-demo-token"

func hashOf(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

type stubStore struct{ em types.Emitter }

func (s stubStore) GetEmitter(context.Context, string) (types.Emitter, error) { return s.em, nil }

type stubBus struct{}

func (stubBus) PublishEmitterEvent(context.Context, types.EmitterEvent) error { return nil }

// post drives the REAL handler against a stubbed store, so what is under test is the door an
// alert source actually hits rather than a helper beside it.
func post(t *testing.T, em types.Emitter, header, value string) int {
	t.Helper()
	in := &Ingest{log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		store: stubStore{em: em}, bus: stubBus{}}
	req := httptest.NewRequest(http.MethodPost, "/emitters/"+em.Name, bytes.NewReader([]byte(`{"a":1}`)))
	if header != "" {
		req.Header.Set(header, value)
	}
	rec := httptest.NewRecorder()
	in.Handler().ServeHTTP(rec, req)
	return rec.Code
}

// THE REGRESSION THAT MATTERS: every Emitter that shipped before this field existed declares no
// token block, and must keep authenticating exactly as it did.
func TestTheDefaultHeaderStillWorksWithNoDeclaration(t *testing.T) {
	em := types.Emitter{Name: "hooks", Kind: types.EmitterWebhook, TokenHash: hashOf(demoToken)}
	if code := post(t, em, types.DefaultTokenHeader, demoToken); code != http.StatusAccepted {
		t.Fatalf("the default header must still authenticate: HTTP %d", code)
	}
	if code := post(t, em, "X-Gitlab-Token", demoToken); code != http.StatusUnauthorized {
		t.Errorf("an undeclared header must NOT authenticate: HTTP %d", code)
	}
}

// A declared header REPLACES the default; it does not widen what is accepted. A declaration that
// added an accepted header would be worse than the gap it closes.
func TestADeclaredHeaderReplacesRatherThanWidens(t *testing.T) {
	em := types.Emitter{Name: "gitlab", Kind: types.EmitterWebhook, TokenHash: hashOf(demoToken),
		Token: &types.TokenSpec{Header: "X-Gitlab-Token"}}

	if code := post(t, em, "X-Gitlab-Token", demoToken); code != http.StatusAccepted {
		t.Fatalf("the declared header must authenticate: HTTP %d", code)
	}
	if code := post(t, em, types.DefaultTokenHeader, demoToken); code != http.StatusUnauthorized {
		t.Errorf("once a header is declared the default must stop working: HTTP %d", code)
	}
	if code := post(t, em, "", ""); code != http.StatusUnauthorized {
		t.Errorf("no token at all: HTTP %d", code)
	}
}

// A declared prefix is stripped before the comparison — "Bearer <token>" is one real spelling.
func TestADeclaredPrefixIsStripped(t *testing.T) {
	em := types.Emitter{Name: "bearer", Kind: types.EmitterWebhook, TokenHash: hashOf(demoToken),
		Token: &types.TokenSpec{Header: "Authorization", Prefix: "Bearer "}}
	if code := post(t, em, "Authorization", "Bearer "+demoToken); code != http.StatusAccepted {
		t.Fatalf("a prefixed token must authenticate: HTTP %d", code)
	}
	// …and the bare token without the declared prefix must not: the declaration says what the
	// source sends, so anything else is a different caller.
	if code := post(t, em, "Authorization", demoToken); code != http.StatusUnauthorized {
		t.Errorf("the prefix is part of what was declared: HTTP %d", code)
	}
	if code := post(t, em, "Authorization", "Bearer wrong-token"); code != http.StatusUnauthorized {
		t.Errorf("a wrong token behind a right prefix: HTTP %d", code)
	}
}
