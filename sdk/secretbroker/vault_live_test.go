package secretbroker

import (
	"context"
	"os"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// TestWithMaterial_VaultLive resolves a REAL secret from a running OpenBao/Vault via
// the actual SDK vault backend (ADR-0094) — the dev proof that the per-call credential
// seam works against the real store, not a mock. Gated on STRATT_LIVE_VAULT_ADDR so it
// is a no-op in normal `task ci`; run it against the dev OpenBao with:
//
//	STRATT_LIVE_VAULT_ADDR=http://localhost:8200 STRATT_LIVE_VAULT_TOKEN=stratt-dev-root \
//	  go test ./secretbroker/ -run VaultLive -v
//
// It reads secret/demo/aws seeded by deploy/dev/openbao-bootstrap.sh.
func TestWithMaterial_VaultLive(t *testing.T) {
	addr := os.Getenv("STRATT_LIVE_VAULT_ADDR")
	if addr == "" {
		t.Skip("set STRATT_LIVE_VAULT_ADDR (+ STRATT_LIVE_VAULT_TOKEN) to run the live OpenBao proof")
	}
	r := New(fake.NewSimpleClientset(), "", WithVault(addr, os.Getenv("STRATT_LIVE_VAULT_TOKEN")))
	ref := &pluginv1.ResolvedRef{
		Vault: &pluginv1.VaultCoords{Mount: "secret", Path: "demo/aws", KvV2: true},
		Keys: []*pluginv1.ResolvedKey{
			{Key: "access_key", Name: "AWS_ACCESS_KEY_ID"},
			{Key: "secret_key", Name: "AWS_SECRET_ACCESS_KEY"},
		},
	}
	var gotAccess string
	var gotSecretLen int
	if err := r.WithMaterial(context.Background(), ref, func(m Material) error {
		gotAccess = m.GetString("AWS_ACCESS_KEY_ID")
		gotSecretLen = len(m.Get("AWS_SECRET_ACCESS_KEY"))
		return nil
	}); err != nil {
		t.Fatalf("live vault resolve failed: %v", err)
	}
	if gotAccess != "AKIADEMOKEY000000000" {
		t.Fatalf("live access_key wrong: %q", gotAccess)
	}
	if gotSecretLen == 0 {
		t.Fatal("live secret_key must be non-empty (and is never logged)")
	}
	t.Logf("LIVE OK: resolved access_key=%s + secret_key(%d bytes) from real OpenBao, zeroized after use", gotAccess, gotSecretLen)
}
