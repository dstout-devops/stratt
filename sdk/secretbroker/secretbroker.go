// Package secretbroker resolves a use-checked CredentialRef to material for a plugin
// that needs PER-CALL credentials (ADR-0052). The core hands the plugin Secret
// COORDINATES (a ResolvedRef — name/namespace/keys, NEVER material); this resolver
// reads the K8s Secret with the plugin's own confined RBAC (MF-A) and enforces
// STRUCTURAL per-Invoke ephemerality (MF-B): material is resolved per call, handed to
// a single use closure, and ZEROIZED before the resolver returns — it can never
// outlive the use, never be cached, never be logged (§2.5).
package secretbroker

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// Resolver reads Secret material by coordinate. It holds NO material state between
// calls — every WithMaterial does a fresh read (MF-B: never cache across calls).
type Resolver struct {
	client    kubernetes.Interface
	namespace string // fallback namespace when a ResolvedRef omits one (the plugin's own)
}

// New builds a Resolver over a K8s client. defaultNamespace is used when a
// ResolvedRef carries no secret_namespace (it must still be within the plugin's
// confined RBAC — MF-A).
func New(client kubernetes.Interface, defaultNamespace string) *Resolver {
	return &Resolver{client: client, namespace: defaultNamespace}
}

// Material is the resolved credential, scoped to a single WithMaterial call. Its bytes
// are zeroized when that call returns — a reference kept past it points at zeros.
type Material struct{ vals map[string][]byte }

// Get returns the bytes for a logical key (the ResolvedKey.name the core authorized),
// or nil. The returned slice is the live buffer — do not retain it past WithMaterial.
func (m Material) Get(key string) []byte { return m.vals[key] }

// GetString is a convenience for text credentials (url/token).
func (m Material) GetString(key string) string { return string(m.vals[key]) }

func (m Material) zero() {
	for _, b := range m.vals {
		for i := range b {
			b[i] = 0
		}
	}
}

// WithMaterial resolves ref, invokes use with the material, and ZEROIZES it before
// returning — the material cannot outlive the single use (MF-B, structural). A
// ResolvedRef with no coordinates (a relay withheld them — MF-C) fails closed. The
// material is scoped to THIS call only; nothing is cached, nothing is logged.
func (r *Resolver) WithMaterial(ctx context.Context, ref *pluginv1.ResolvedRef, use func(Material) error) error {
	if ref == nil || ref.GetSecretName() == "" {
		return fmt.Errorf("secretbroker: no resolved coordinates for this credential (withheld or unresolved) — cannot resolve, failing closed")
	}
	ns := ref.GetSecretNamespace()
	if ns == "" {
		ns = r.namespace
	}
	sec, err := r.client.CoreV1().Secrets(ns).Get(ctx, ref.GetSecretName(), metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("secretbroker: read secret %s/%s: %w", ns, ref.GetSecretName(), err)
	}
	m := Material{vals: map[string][]byte{}}
	defer m.zero() // MF-B: material is zeroized before this call returns, always
	for _, k := range ref.GetKeys() {
		// The plugin reads ONLY the data keys the core authorized (the ResolvedKey set,
		// mirroring the CredentialRef Injection). name is the logical handle the plugin
		// asks for; key is the Secret data key it lives under.
		raw, ok := readKey(sec, k.GetKey())
		if !ok {
			return fmt.Errorf("secretbroker: secret %s/%s has no key %q", ns, ref.GetSecretName(), k.GetKey())
		}
		logical := k.GetName()
		if logical == "" {
			logical = k.GetKey()
		}
		m.vals[logical] = raw
	}
	return use(m)
}

// readKey returns a Secret data value (copied into a fresh buffer the resolver owns
// and zeroizes), preferring Data (raw) then StringData.
func readKey(sec *corev1.Secret, key string) ([]byte, bool) {
	if v, ok := sec.Data[key]; ok {
		return append([]byte(nil), v...), true
	}
	if v, ok := sec.StringData[key]; ok {
		return append([]byte(nil), []byte(v)...), true
	}
	return nil, false
}
