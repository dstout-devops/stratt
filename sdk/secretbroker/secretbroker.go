// Package secretbroker resolves a use-checked CredentialRef to material for a plugin
// that needs PER-CALL credentials (ADR-0052). The core hands the plugin Secret
// COORDINATES (a ResolvedRef — NEVER material); this resolver reads the material with
// the plugin's OWN confined identity (MF-A) and enforces STRUCTURAL per-Invoke
// ephemerality (MF-B): material is resolved per call, handed to a single use closure,
// and ZEROIZED before the resolver returns — it can never outlive the use, never be
// cached, never be logged (§2.5).
//
// Two backends resolve behind one WithMaterial entrypoint, dispatched by which
// coordinate the ResolvedRef carries (ADR-0094):
//   - k8s-secret — read a K8s Secret with the plugin's confined RBAC.
//   - vault      — read an OpenBao/Vault KV secret with the plugin's own token/role.
//
// The shared entrypoint owns MF-B (zeroize) and MF-C (no coordinates ⇒ fail closed)
// for BOTH backends; each backend only reads raw field bytes.
package secretbroker

import (
	"context"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// Env vars a plugin sets to opt its resolver into the vault backend (MF-A: the
// plugin authenticates to OpenBao AS ITSELF, under a policy scoped to the granted
// paths). A static token is dev-only; production brokers AppRole/K8s-auth (ADR-0094).
const (
	EnvVaultAddr  = "STRATT_SECRETBROKER_VAULT_ADDR"
	EnvVaultToken = "STRATT_SECRETBROKER_VAULT_TOKEN"
)

// Resolver reads Secret material by coordinate. It holds NO material state between
// calls — every WithMaterial does a fresh read (MF-B: never cache across calls).
type Resolver struct {
	client    kubernetes.Interface
	namespace string       // fallback namespace when a k8s ResolvedRef omits one (the plugin's own)
	vault     *vaultClient // nil unless the plugin opted into the vault backend (WithVault*)
}

// Option configures a Resolver (backend opt-ins).
type Option func(*Resolver)

// WithVault attaches an OpenBao/Vault KV backend addressed at addr, authenticating
// with token — the plugin's own identity (MF-A). A vault-coordinate ResolvedRef
// arriving at a Resolver with no vault client fails closed.
func WithVault(addr, token string) Option {
	return func(r *Resolver) {
		if addr != "" {
			r.vault = newVaultClient(addr, token)
		}
	}
}

// WithVaultFromEnv attaches the vault backend iff STRATT_SECRETBROKER_VAULT_ADDR is
// set (a no-op otherwise) — the standard wiring for a plugin main().
func WithVaultFromEnv() Option {
	return WithVault(os.Getenv(EnvVaultAddr), os.Getenv(EnvVaultToken))
}

// New builds a Resolver over a K8s client. defaultNamespace is used when a k8s
// ResolvedRef carries no secret_namespace (it must still be within the plugin's
// confined RBAC — MF-A). Pass WithVault*/options to enable additional backends.
func New(client kubernetes.Interface, defaultNamespace string, opts ...Option) *Resolver {
	r := &Resolver{client: client, namespace: defaultNamespace}
	for _, o := range opts {
		o(r)
	}
	return r
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

// logicalName is the handle the plugin asks for: the ResolvedKey.name the core
// authorized, falling back to the backend field key when name is unset.
func logicalName(k *pluginv1.ResolvedKey) string {
	if n := k.GetName(); n != "" {
		return n
	}
	return k.GetKey()
}

// WithMaterial resolves ref, invokes use with the material, and ZEROIZES it before
// returning — the material cannot outlive the single use (MF-B, structural). Dispatch
// is by coordinate kind (ADR-0094): exactly one of {vault, k8s-secret} is populated;
// NEITHER (a relay withheld them — MF-C, or an unresolved ref) fails closed. The
// material is scoped to THIS call only; nothing is cached, nothing is logged.
func (r *Resolver) WithMaterial(ctx context.Context, ref *pluginv1.ResolvedRef, use func(Material) error) error {
	if ref == nil {
		return fmt.Errorf("secretbroker: no resolved coordinates for this credential (withheld or unresolved) — cannot resolve, failing closed")
	}
	switch {
	case ref.GetVault() != nil:
		return r.withVaultMaterial(ctx, ref, use)
	case ref.GetSecretName() != "":
		return r.withK8sMaterial(ctx, ref, use)
	default:
		// Neither coordinate set — withheld across a relay (MF-C) or unresolved.
		return fmt.Errorf("secretbroker: no resolved coordinates for this credential (withheld or unresolved) — cannot resolve, failing closed")
	}
}

// withK8sMaterial reads the authorized data keys from a K8s Secret (the plugin's own
// confined RBAC, MF-A), maps them to their logical names, and hands them to use.
func (r *Resolver) withK8sMaterial(ctx context.Context, ref *pluginv1.ResolvedRef, use func(Material) error) error {
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
		// mirroring the CredentialRef Injection).
		raw, ok := readKey(sec, k.GetKey())
		if !ok {
			return fmt.Errorf("secretbroker: secret %s/%s has no key %q", ns, ref.GetSecretName(), k.GetKey())
		}
		m.vals[logicalName(k)] = raw
	}
	return use(m)
}

// withVaultMaterial reads the authorized fields from an OpenBao/Vault KV secret (the
// plugin's own token/role, MF-A), maps them to their logical names, and hands them to
// use. A vault ResolvedRef at a Resolver with no vault client fails closed.
func (r *Resolver) withVaultMaterial(ctx context.Context, ref *pluginv1.ResolvedRef, use func(Material) error) error {
	if r.vault == nil {
		return fmt.Errorf("secretbroker: credential carries vault coordinates but this plugin has no vault backend configured (set %s) — failing closed", EnvVaultAddr)
	}
	coords := ref.GetVault()
	fields := make([]string, 0, len(ref.GetKeys()))
	for _, k := range ref.GetKeys() {
		fields = append(fields, k.GetKey())
	}
	// readKV returns fresh []byte the resolver OWNS (decoded without an intermediate
	// Go string — MF-B: they must be zeroizable, ADR-0094).
	raw, err := r.vault.readKV(ctx, coords, fields)
	if err != nil {
		return fmt.Errorf("secretbroker: vault read %s/%s: %w", coords.GetMount(), coords.GetPath(), err)
	}
	m := Material{vals: map[string][]byte{}}
	defer m.zero() // MF-B
	for _, k := range ref.GetKeys() {
		b, ok := raw[k.GetKey()]
		if !ok {
			return fmt.Errorf("secretbroker: vault secret %s/%s has no field %q", coords.GetMount(), coords.GetPath(), k.GetKey())
		}
		m.vals[logicalName(k)] = b
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
