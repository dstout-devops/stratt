# ADR 0099 — OpenBao KV metadata Syncer: secret existence/metadata as a projection, never material

- **Status:** **Accepted** (2026-07-23, steward) — charter-guardian **PASS-WITH-CHANGES** (metadata-only
  is the right §1.2/§2.5 line, write-path-trigger-enforced; the client's no-data-method is the correct
  structural guarantee, mirroring ADR-0009's no-material-column). Findings folded: (1) **defense-in-depth
  at the policy layer** — the plugin's OpenBao token/policy must `read` `{mount}/metadata/*` and **deny/
  omit `{mount}/data/*`** (two independent guarantees: client + ACL); (2) **sensitive-by-aggregation** —
  the secret-path *map* is a recon artifact (path names disclose vendors/customers/architecture), so the
  `kv-secret` kind / `kv.metadata` Facet is View/ReBAC-scoped at least as tightly as general Entities,
  ideally tighter; (3) **§1.8 no silent under-count** — a partial enumeration would false-tombstone
  secrets, so a metadata-read failure **fails the sync loud**, never a partial full-sync; (4) kind
  hardened `secret` → **`kv-secret`** (disambiguates from CredentialRef). vocabulary-linter **PASS**.
- **Date:** 2026-07-23
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.2 (projections, never a second truth — metadata ONLY, never secret material) ·
  §2.5 (secrets brokered, never held by the core) · §1.1 (type the seams) · §2 (vocabulary) · folds
  into `plugins/openbao` (ADR-0098); mirrors the metadata-only discipline of ADR-0097 (awss3 buckets)
  and the material-free Observe of ADR-0052 (SecretBroker).

## Context

`plugins/openbao` now hosts the PKI surfaces (ADR-0098). The remaining OpenBao surface is the **KV
store** — the same engine the SecretBroker vault backend (ADR-0094) resolves material from. Operators
want the estate to *know secrets exist* — their paths, versions, and age — for coverage/rotation/orphan
questions ("which secrets haven't rotated in 90 days", "what does this app depend on"). But the graph
must **never** hold the secret *values*.

**The load-bearing invariant:** the Syncer reads **`{mount}/metadata/*`** (paths, version counts,
timestamps) and **NEVER `{mount}/data/*`** (the values). This is the §1.2 projection line for a secret
store — the graph is a rebuildable read-model of *what secrets exist*, not a copy of their contents.
It is the exact analogue of ADR-0097's bucket-metadata-not-object-bytes and the strongest §2.5 posture
(material never even enters the plugin process on this path).

**In scope:** a metadata-only KV Syncer surface on `plugins/openbao` (a new `secret` Entity kind +
`kv.metadata` Facet), enabled when a KV mount is configured. **Out of scope:** any read of secret data
(a hard invariant, not a scope line); KV *write* Actions (the KV store's writes are the operator's /
the SecretBroker's concern); lease/policy modeling (a follow-up).

## Decision

Add a KV metadata Syncer to `plugins/openbao`, observing a configured KV v2 mount:

1. **Entity kind `secret`** — one per KV secret path. Identity `kv.path` = `{mount}/{path}` (e.g.
   `secret/demo/aws`). This is the *observed external secret's metadata*, distinct from a
   `CredentialRef` (Stratt's pointer Named Kind) — the graph records that the secret EXISTS, never its
   value.
2. **Facet `kv.metadata`** (closed, §1.1): `{mount, path, currentVersion, createdTime, updatedTime}` —
   read from `{mount}/metadata/{path}`. **No data field, ever.** Co-fidelity tested against the
   normalizer.
3. **Observe (metadata-only, §1.2/§2.5):** LIST `{mount}/metadata` recursively for paths; for each,
   GET `{mount}/metadata/{path}` (version info + timestamps). The plugin **NEVER** calls
   `{mount}/data/{path}` on this path — enforced by the client surface exposing only a metadata reader,
   so a future refactor cannot accidentally read values.
4. **Registration:** enabled when `STRATT_OPENBAO_KV_MOUNT` is set (opt-in, like the Syncer interval);
   the grant adds the `kv.metadata` Facet + `kv.path` identity/tombstone. The plugin's existing PKI
   Observe and this KV Observe coexist on the one openbao Source (each projects its own kinds).

## Charter alignment

- **§1.2 / §2.5 — the whole point.** Secret *metadata* is a projection of external reality; secret
  *values* are never read, stored, or transited. The graph knows secrets exist (coverage/rotation
  queries) without becoming a secret store. The client surface makes the values *unreadable on this
  path* (no data method), so the invariant is structural, not a review norm.
- **§1.1.** One closed `kv.metadata` Facet demanded by the shipping Syncer; co-fidelity tested.
- **§2 (vocabulary).** `secret` Entity kind (cloud-native, not a Named Kind, not banned; disambiguated
  from CredentialRef in the code). `kv.path`/`kv.metadata` are `namespace.concept`. vocabulary-linter
  gate.
- **§1.4.** No new dependency — the hand-rolled OpenBao `net/http` client is extended with metadata
  reads only.

## Consequences

- **Positive:** the estate gains secret-coverage/rotation visibility with zero material exposure;
  `plugins/openbao` is now the full OpenBao home (PKI + KV); the metadata-only Syncer template
  (awss3, this) is proven twice.
- **Negative / trade-offs:** LIST-then-GET-metadata is O(N) over secret paths (a large KV store is
  many reads — bounded later by path-prefix scoping); KV v1 (no metadata endpoint) is unsupported
  (v2-only, stated).
- **Follow-ups:** lease/policy observation; a rotation-age Baseline/Finding on `kv.metadata.updatedTime`;
  path-prefix scoping for large stores.

## Alternatives considered

- **Read secret values to project richer Facets.** Rejected — a direct §1.2/§2.5 violation; the graph
  must never hold material. Metadata-only is the invariant.
- **A separate `plugins/openbao-kv` plugin.** Rejected — it is the same Source/backend/token as the PKI
  surface; ADR-0098 consolidated OpenBao surfaces into one plugin. A second Observe surface on the one
  plugin is the clean shape.
- **Project secrets as `CredentialRef`s.** Rejected — a CredentialRef is Stratt's *desired-state
  pointer* (Git-declared); an observed KV secret is external reality (a projection). Conflating them
  would make the read-model a second source of truth for credentials (§1.2 violation).
