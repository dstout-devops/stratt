package pluginserve

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"sync"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// ── contract pinning (port invariant #5) ─────────────────────────────────────
//
// A ContractRef pins a schema by id AND content hash, and the port's fifth invariant is that
// "schema drift is blocking, never silently absorbed". Until now every plugin populated `schema_id`
// and left `sha256` EMPTY, so the invariant had nothing to check: core validated a Step against its
// own copy of a document, the plugin conformance-checked args against its copy, and NOTHING
// compared them. Two copies of a schema that no one compares is the definition of silent drift.
//
// It could not have been fixed before ADR-0138 D4. A plugin cannot hash a document it does not
// have, and self contracts lived in the core binary. Now they ship WITH the plugin, so the plugin
// can embed its own tree and advertise the digest of exactly the bytes it will enforce.
//
// The comparison happens core-side at registration: core hashes the document IT holds and refuses
// a provider whose advertisement disagrees. That is the whole loop — the plugin states what it
// enforces, core states what it validates, and a mismatch is refused rather than discovered when a
// Step fails against a schema nobody thought had changed.

// ContractSet hashes a plugin's own embedded contract documents on demand.
//
// Bind it to the tree the plugin ships:
//
//	//go:embed contracts
//	var contractFS embed.FS
//	var contracts = pluginserve.Contracts(contractFS)
//
// then build refs by contract id — the SAME id core keys on, which is the embedded path minus
// `contracts/` and `.schema.json`.
type ContractSet struct {
	fsys fs.FS
	mu   sync.Mutex
	hash map[string]string
}

// Contracts binds a ContractSet to an embedded (or any) filesystem rooted at the plugin module.
func Contracts(fsys fs.FS) *ContractSet {
	return &ContractSet{fsys: fsys, hash: map[string]string{}}
}

// Ref returns the pinned ContractRef for a contract id, e.g. "actions/helm/deploy.input".
//
// A MISSING or unreadable document yields a ref with an EMPTY sha256 rather than a panic, and the
// choice is deliberate: a plugin must still start and still serve. Core treats an unpinned ref as
// unverifiable and says so — which is a diagnosable state — whereas a plugin that refused to boot
// over its own packaging would take the whole Actuator down for a check that is meant to protect it.
func (c *ContractSet) Ref(id string) *pluginv1.ContractRef {
	return &pluginv1.ContractRef{SchemaId: id, Sha256: c.Sum(id)}
}

// Sum returns the hex sha256 of the document behind id, or "" if it cannot be read. Memoized —
// GetManifest is called on every verification pass, and re-hashing a fixed embedded file each time
// is work with no answer that can change.
func (c *ContractSet) Sum(id string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if h, ok := c.hash[id]; ok {
		return h
	}
	h := ""
	if raw, err := fs.ReadFile(c.fsys, "contracts/"+id+".schema.json"); err == nil {
		sum := sha256.Sum256(raw)
		h = hex.EncodeToString(sum[:])
	}
	c.hash[id] = h
	return h
}
