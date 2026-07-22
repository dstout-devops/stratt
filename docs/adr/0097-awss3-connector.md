# ADR 0097 — The awss3 Connector: bucket lifecycle Actions + metadata-only bucket Syncer

- **Status:** **Accepted** (2026-07-22, steward) — charter-guardian **PASS-WITH-CHANGES**
  (metadata-only is the correct §1.2 line, DB-trigger-enforced; opaque policy param resists ontology
  creep §1.1; no Evidence-store model collision). Findings folded: (1) best-effort tagging/versioning
  no-ops now log a **visible Warn** (§1.8 — a dropped managed-stamp is diagnosed, not silent);
  (2) **`delete-bucket`/`put-bucket-policy` guard the Evidence WORM bucket** — a protected-bucket set
  (STRATT_AWSS3_PROTECTED_BUCKETS + the evidence bucket) that those Actions refuse, so the connector
  can never be the hole in ADR-0029's write-once story; (3) `bucket.config.versioning` is optional
  (a non-reporting backend emits no false `false`); (4) separate-plugin rationale re-cited to §2.2
  (Connector = one Source), not ADR-0059. vocabulary-linter **PASS**.
- **Date:** 2026-07-22
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.2 (projections, never a second truth — metadata only, NEVER object bytes) ·
  §1.1 (type the seams) · §1.4 (boring spine — reuse the already-vetted s3 SDK) · §1.5 (sovereign
  contracts) · §2 (vocabulary) · builds on ADR-0046 (plugin port), ADR-0093 (SeaweedFS dev backend),
  ADR-0095/0096 (the awsec2 connector shape it mirrors).

## Context

The AWS side has real compute + network/storage EC2 coverage (ADR-0095/0096). The remaining core AWS
seam is **object storage (S3)**. Unlike EC2 (one connector, many kinds), S3 is a **separate Source /
identity / lifecycle**, so it is a **new plugin `plugins/awss3`**, not an extension of awsec2 — per **§2.2**
(a Connector is the versioned integration package for *one* Source; a distinct Source
gets a distinct Connector).

**The load-bearing §1.2 boundary for object storage:** the graph projects **bucket metadata only** —
existence, region, versioning, creation date — and **NEVER object contents**. The objects are the
external system-of-record's data; the graph is a rebuildable read-model of the *estate topology*, not
a copy of the data plane. This is the same discipline the future KV-metadata Syncer (Slice G) follows.

**No new dependency (§1.4):** `aws-sdk-go-v2/service/s3 v1.105.0` is already vetted (dependency-scout
2026-07-13) and used by `core/internal/evidencestore` against SeaweedFS. The plugin reuses that exact
client pattern (`s3.NewFromConfig` + `BaseEndpoint` + `UsePathStyle`). Dev backend: SeaweedFS
(ADR-0093), already up on `:8333`.

**In scope:** a new `awss3` Connector (SYNCER + INVOKE) — a metadata-only bucket Syncer + bucket
lifecycle Actions (create / delete / enable-versioning / put-policy).

**Out of scope:** object-level Actions (put/get/delete object — the data plane, deliberately not
modeled); S3 Object Lock / WORM (that is the Evidence store's concern, ADR-0029); bucket *objects* as
Entities; cross-region replication; a full bucket-policy model (the policy is an opaque param).

## Decision

Ship `plugins/awss3` mirroring the awsec2 plugin structure (its own Go module, `cmd/stratt-plugin-awss3`,
`Server` embedding `UnimplementedPluginServiceServer`, inject-a-fake-`S3API` tests), advertising SYNCER
class with OBSERVE + INVOKE:

1. **Bucket Syncer (Observe, metadata-only §1.2).** `ListBuckets` → one `bucket` Entity per bucket:
   - identity `aws.bucketArn` = `arn:aws:s3:::<name>` (globally unique, region-independent)
   - Facet `bucket.config` (closed): `{creationDate}` — plus best-effort `versioning` from
     `GetBucketVersioning` when the backend supports it.
   - label `stratt.managed=true` when the bucket carries the `stratt:managed` tag (best-effort
     `GetBucketTagging`) — the same anti-orphan story as ADR-0095, tolerant of backends that lack
     tagging. **Never** lists or reads objects.

2. **Bucket lifecycle Actions (Invoke, each its own input/output Contract):**
   - `awss3/create-bucket` `{name, region?}` → `CreateBucket`; best-effort `PutBucketTagging`
     stamps `stratt:managed` (anti-orphan, tolerated-if-unsupported). Output `{bucketArn}`.
   - `awss3/delete-bucket` `{name}` → `DeleteBucket`. Output `{name}`.
   - `awss3/enable-versioning` `{name}` → `PutBucketVersioning(Enabled)`. Output `{name, versioning}`.
   - `awss3/put-bucket-policy` `{name, policy}` → `PutBucketPolicy` (policy is an opaque JSON string
     param — no policy model). Output `{name}`.
   Each is gated by its CredentialRef use-check (§2.5); credentials are the SDK chain, never params.

3. **Facet Contract (data, §1.1).** `contracts/facets/bucket.config.schema.json` (closed) + the four
   `contracts/actions/awss3/*.{input,output}.schema.json`. A co-fidelity test asserts the Syncer's
   emitted `bucket.config` keys are a subset of the closed schema (the ADR-0095 flag-2 discipline).

4. **Registration.** A new `awss3` grant in strattd (identity/tombstone `aws.bucketArn`, Facet
   `bucket.config`, label keys `aws.region`/`stratt.managed`), the four Actions on one host, and the
   Syncer wired opt-in via `STRATT_AWSS3_INTERVAL` (mirrors the awsec2 Block-A/Block-B split).

## Charter alignment

- **§1.2 — the whole point.** Bucket *metadata* is a projection of external reality; object *bytes*
  are never read, listed, or stored — the graph is topology, not a data copy. Run-provenance on
  Actions, Source-provenance on the Syncer; no second writer.
- **§1.1.** One narrow `bucket.config` Facet demanded by the shipping Syncer Contract, closed schema.
- **§1.4.** Zero new dependencies — the s3 SDK is already in the tree and vetted.
- **§1.5.** Typed input/output Contracts per Action, pinned + drift-checked; the plugin introduces no
  schema.
- **§2.** New Entity kind `bucket` (cloud-native, not a Named Kind, not a banned term); identity
  `aws.bucketArn`; Actions `awss3/*`. vocabulary-linter gate.
- **§2.5.** Actions gated by CredentialRef; the plugin resolves the SDK cred chain in-process, never
  through the core.

## Consequences

- **Positive:** completes the core AWS surface (compute + network/storage + object storage); a clean
  metadata-only Syncer template for the KV-metadata Syncer (Slice G); no new dependency.
- **Negative / trade-offs:** SeaweedFS supports a *subset* of the S3 control API — versioning/policy/
  tagging are best-effort in dev and may be no-ops (the Actions are round-trip-proven against the real
  backend where supported, deep-semantics-proven against real AWS). Object-plane operations are out of
  scope by design.
- **Follow-ups:** object-plane Actions if a real use case appears (still never *projecting* objects);
  a richer bucket-policy model; bucket→region/account Relations.

## Alternatives considered

- **Extend awsec2 with S3.** Rejected — different Source/identity/lifecycle; ADR-0059 says separate
  connectors. A new plugin keeps the module boundary clean.
- **Project objects as Entities.** Rejected — a direct §1.2 violation (the graph would copy the data
  plane); metadata-only is the invariant.
- **A typed bucket-policy Facet/model.** Deferred — the policy is an opaque param for now; modeling IAM
  policy is its own large effort.
