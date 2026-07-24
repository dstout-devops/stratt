package types

import "encoding/json"

// CredentialRef backend kinds (ADR-0009). All three are valid pointers from
// day one; resolving vault and workload-identity arrives in their own slices.
const (
	BackendK8sSecret        = "k8s-secret"
	BackendVault            = "vault"
	BackendWorkloadIdentity = "workload-identity"
)

// Injection modes. File is preferred (env vars leak via /proc, crash dumps,
// and child-process inheritance — ADR-0009); env remains available where
// tools demand it. There is no free-templating mode by design.
const (
	InjectEnv  = "env"
	InjectFile = "file"
)

// CredentialRef is a pointer + injection policy to brokered secret material
// (charter §2.5). Material NEVER persists in the platform: nothing in this
// type — or its table — can hold a secret. The control plane composes pod
// specs from it; the kubelet (or an in-pod agent) resolves the material.
type CredentialRef struct {
	// Name is the stable reference Steps and Sources bind by.
	Name string `json:"name"`
	// OwnerTeam scopes ownership (org → team → Principal, ADR-0009). There
	// are no user-private credentials; a personal credential is a team of
	// one.
	OwnerTeam string `json:"ownerTeam"`
	// Backend is the broker kind: k8s-secret | vault | workload-identity.
	Backend string `json:"backend"`
	// Locator addresses the material inside the backend — backend-shaped
	// data (k8s-secret: {"namespace": ..., "name": ...}).
	Locator json.RawMessage `json:"locator"`
	// Injection is the per-field projection policy. Empty ⇒ the ref brokers NO
	// material (a gate-only ref, ADR-0052/0092) — legal ONLY when GateOnly asserts
	// that emptiness is deliberate.
	Injection []CredentialInjection `json:"injection"`
	// GateOnly asserts an injection-less ref is DELIBERATELY material-less — purely
	// an Action's authz gate. It keeps an accidentally-dropped injection block failing
	// at apply (§1.8) rather than silently degrading to "resolves nothing", on the
	// API door as well as the Git door. Informational (behavior follows empty
	// Injection); carried so the wire path can validate intent as ParseDir does.
	GateOnly bool `json:"gateOnly,omitempty"`
	// DeclaredBy mirrors Views: "cac" (Git-declared) or "api".
	DeclaredBy string `json:"declaredBy,omitempty"`
}

// VaultLocator is the parsed CredentialRef.Locator for backend: vault (ADR-0094)
// — a KV COORDINATE, never material. Mount/Path address the secret within an
// OpenBao/Vault store; KVv2 selects the KV v2 (nested `data` wrapper) read shape.
// The plugin resolves the material itself, as itself (§2.5); the core only ever
// composes these coordinates after the use-check.
type VaultLocator struct {
	Mount string `json:"mount"`
	Path  string `json:"path"`
	KVv2  bool   `json:"kvV2"`
}

// CredentialInjection projects one backend field into the execution pod.
type CredentialInjection struct {
	// Key is the field within the backend material (e.g. the Secret data
	// key).
	Key string `json:"key"`
	// As is the projection mode: env | file.
	As string `json:"as"`
	// Name is the env var name (as=env) or the path under the pod's
	// credentials mount (as=file).
	Name string `json:"name"`
}

// Principal is one identity kind for humans, services, and agents alike
// (charter §2.5): all three live in the same authz, audit, and cost model.
type Principal struct {
	ID string `json:"id"`
	// Kind is human | service | agent.
	Kind string `json:"kind"`
}
