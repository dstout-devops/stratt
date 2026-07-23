package openbao

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// fakeTransit is a reversible stand-in for OpenBao Transit (no live server).
type fakeTransit struct{ ensured map[string]bool }

func (f *fakeTransit) EnsureKey(_ context.Context, key string) error {
	if f.ensured == nil {
		f.ensured = map[string]bool{}
	}
	f.ensured[key] = true
	return nil
}
func (f *fakeTransit) Encrypt(_ context.Context, key string, pt []byte) ([]byte, int, error) {
	return append([]byte("wrapped:"+key+":"), pt...), 3, nil
}
func (f *fakeTransit) Decrypt(_ context.Context, key string, ct []byte) ([]byte, error) {
	p := []byte("wrapped:" + key + ":")
	if !bytes.HasPrefix(ct, p) {
		return nil, fmt.Errorf("wrong key")
	}
	return ct[len(p):], nil
}

func transitServer(t *testing.T, tr Transit) *Server {
	t.Helper()
	s := NewServer(Config{Addr: "http://x", Token: "t"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.newTransit = func(context.Context) (Transit, error) { return tr, nil }
	return s
}

// TestWrapUnwrapKey proves the KeyCustodian RPCs: WrapKey ensures the per-domain key and
// wraps the DEK (never returning it plainly); UnwrapKey recovers it. The manifest
// advertises the capability.
func TestWrapUnwrapKey(t *testing.T) {
	ft := &fakeTransit{}
	srv := transitServer(t, ft)

	dek := []byte("0123456789abcdef0123456789abcdef")
	w, err := srv.WrapKey(context.Background(), &pluginv1.WrapKeyRequest{Domain: "india", Dek: dek})
	if err != nil {
		t.Fatalf("wrapkey: %v", err)
	}
	if bytes.Equal(w.GetWrapped(), dek) {
		t.Fatal("wrapped bytes must not be the plain DEK")
	}
	if w.GetKeyVersion() != 3 || !ft.ensured["stratt-india"] {
		t.Fatalf("per-domain key not ensured / version wrong: ensured=%v ver=%d", ft.ensured, w.GetKeyVersion())
	}
	u, err := srv.UnwrapKey(context.Background(), &pluginv1.UnwrapKeyRequest{Wrapped: w.GetWrapped(), Domain: "india"})
	if err != nil || !bytes.Equal(u.GetDek(), dek) {
		t.Fatalf("unwrapkey round-trip: %v", err)
	}

	// The manifest advertises the keycustodian capability.
	m, _ := srv.GetManifest(context.Background(), &pluginv1.GetManifestRequest{})
	has := false
	for _, c := range m.GetManifest().GetCapabilities() {
		if c == "keycustodian" {
			has = true
		}
	}
	if !has {
		t.Errorf("manifest must advertise the keycustodian capability: %v", m.GetManifest().GetCapabilities())
	}
}

// TestLiveTransitAgainstOpenBao proves WrapKey/UnwrapKey against REAL OpenBao Transit
// (ADR-0100 F2): the DEK is wrapped by a KEK that never leaves OpenBao (the wrapped bytes
// are a Vault ciphertext, not the DEK), and unwraps back. Gated on STRATT_LIVE_OPENBAO_ADDR.
func TestLiveTransitAgainstOpenBao(t *testing.T) {
	addr := os.Getenv("STRATT_LIVE_OPENBAO_ADDR")
	if addr == "" {
		t.Skip("set STRATT_LIVE_OPENBAO_ADDR to run the live Transit proof")
	}
	srv := NewServer(Config{Addr: addr, Token: os.Getenv("STRATT_LIVE_OPENBAO_TOKEN")},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	dek := []byte("0123456789abcdef0123456789abcdef") // 32 bytes
	w, err := srv.WrapKey(context.Background(), &pluginv1.WrapKeyRequest{Domain: "default", Dek: dek})
	if err != nil {
		t.Fatalf("live WrapKey: %v", err)
	}
	if !bytes.HasPrefix(w.GetWrapped(), []byte("vault:")) {
		t.Fatalf("Transit-wrapped DEK must be a vault ciphertext, got %q", w.GetWrapped())
	}
	if bytes.Contains(w.GetWrapped(), dek) {
		t.Fatal("the plaintext DEK must never appear in the wrapped bytes")
	}
	t.Logf("LIVE Transit-wrapped DEK: %s (key version %d) — KEK never left OpenBao", w.GetWrapped(), w.GetKeyVersion())
	u, err := srv.UnwrapKey(context.Background(), &pluginv1.UnwrapKeyRequest{Wrapped: w.GetWrapped(), Domain: "default"})
	if err != nil || !bytes.Equal(u.GetDek(), dek) {
		t.Fatalf("live UnwrapKey round-trip: %v", err)
	}
	t.Log("LIVE unwrap recovered the DEK")
}
