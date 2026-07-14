# ADR 0036 — `Intent/FileSet` + `Intent/Access` GA (file distribution + host-access governance)

- **Status:** Accepted (Commit 1 — Intent/FileSet; Commit 2 — Intent/Access + recertification Evidence)
- **Date:** 2026-07-14
- **Deciders:** Project steward (dstout)
- **Charter sections:** §8 (Phase-3 Intent-kind GA), §2.4 (Intent kinds, claim types, the anti-GPO
  axiom, the intent→Baseline→Finding→remediation loop), §4.2 (the compiler worked example — the same
  shape carries FileSet/Access), §1.1 (type the seams), §1.2 (projections, never a second truth), §1.4
  (boring spine, no parallel stack), §2.5 (Authorization is CaC; CredentialRef — secrets never in the
  graph), §1.6 (one authorization model, one audit stream), §1.8 (never hide failure); ADR-0023 (the
  compiler), ADR-0030 (Intent/Certificate GA — the schema-at-the-seam machinery this reuses; it parked
  FileSet/Access + `onRemove: revert` as next), ADR-0033 (project-then-observe collector + hand-written
  facet-observation Baselines), ADR-0029 (object-locked Evidence), ADR-0034 (the one audit stream)

## Context

Charter §2.4 names five Intent payload kinds; only `Intent/Application` and `Intent/Certificate` were
implemented. ADR-0030 built the schema-at-the-seam machinery (`Intent.Spec` typed by a
`contracts/intents/<kind>.schema.json`; a kind is "implemented" iff its schema exists) and explicitly
deferred `Intent/FileSet`, `Intent/Access`, and `onRemove: revert` as the next kinds. This slice GAs
both — the last capability build before the Phase-3 promote gate.

The charter pins what each kind *is* (§2.4, §4.2): **`Intent/FileSet`** = "content-addressed …
checksum Facets" (declarative file distribution with drift), **`Intent/Access`** = "additive claims,
per-element provenance" for "local admin groups, sudoers, trust stores" — i.e. **host/OS-level access
state**. Platform authorization is deliberately out of scope: it stays the CaC/OpenFGA tuple spine
(ADR-0009/0028/0035, §2.5). An Intent that minted platform grants would be a second authz truth.

**AAP has neither** a declarative file-drift model (AWX file distribution is imperative `copy`/`template`
tasks with no continuous drift→Finding loop) nor access recertification. Both kinds are open capabilities
enterprises pay for elsewhere.

## Decision

1. **No new engine — two kinds are data + content over the shipped spine (§1.4).** Both compile through
   the existing `compiler.Compile` → facet-observation Baseline → `EvaluateFacetBaseline` → Finding
   path; dispatch is by schema-existence + `Blueprint.For == Intent.Kind`, with **no per-kind switch**.
   A kind is: a spec schema, Facet schema(s), a projection, a Blueprint, gated remediation Workflow
   refs, and `onRemove` wiring. `intentKindFromFile` gained an explicit spelling map so the `fileset`
   schema maps to the frozen §2 kind **`Intent/FileSet`** (not `Intent/Fileset`) — the spelling is API.

2. **The collector is project-then-observe (§1.2/§1.4; the ADR-0033 precedent).** The ansible actuator's
   `ExtractFacts` projects `fileset.content` (from a `stratt_fileset` set_fact: `{key: {digest, mode,
   owner, group, present}}`) and `access.grants` (from `stratt_access`: an array of bare `{subject,
   kind, scope}` tuples) — the side-effect of a scheduled, read-only gather Run through the constrained
   `Projector` (WriterRun provenance), **not a new Syncer** (a host-gather Syncer would be a parallel
   execution stack). No new write path to Entity attributes.

3. **`Intent/FileSet` (Commit 1).** `fileset.content.<key>.digest` is observed `Equals` the Intent's
   expected `{{.spec.digest}}` (the cert-`notBefore` substitution pattern). `key` is a dot-free slug so
   the Facet-path evaluator can address it. **`claim: additive`** — many FileSet Intents contribute
   distinct files to one host's `fileset.content` Facet, each keyed by its own slug; they union rather
   than false-conflict. Two Intents contending over the **same** key with different digests produce two
   per-Assignment Baselines that both survive and both open Findings against the single observed digest
   — no silent last-writer-wins (anti-GPO holds; §2.4). Content is delivered by **source-ref + expected
   digest**; content-addressed OCI artifacts on the Bundle/cosign rails (ADR-0032) are the documented
   next layer. **`onRemove: revert`** surfaces the Blueprint's `removeWorkflow` (a file-absent Workflow)
   on the orphan Finding — a ref the operator launches, never auto-run (§5 Flow 2).

4. **`Intent/Access` (Commit 2).** A host-access grant is an **additive** `access.grants` Facet element
   `{subject, kind, scope}` (`kind` ∈ sudo|group|authorized_key|account); the route observes
   `Contains {{.spec…}}` (ensure-contains). `subject` is a **host-local account, deliberately not the
   platform Principal Named Kind** (§2) — keeping the host/platform identity boundary sharp
   (charter-guardian Flag 2, resolved in-slice). Public keys only; private material never enters the
   graph (§1.2/§2.5). `onRemove: revert|remove` → a gated revoke Workflow on the orphan Finding.

5. **Access recertification → object-locked Evidence + audit (the beat-AAP IGA differentiator, §1.6).**
   `GET /access/recertification/{view}` folds the observed `access.grants` across a View against the
   desired Intent/Access grants — per-element provenance (which Intent/Assignment declared each grant)
   comes from the **desired side in Git** (§1.2), unmanaged/rogue grants are flagged, and `status` is
   `overdue` when the View has never been attested or the last attestation is older than the cadence
   (§1.8). `POST` attests: the reviewed grant set seals as an **object-locked Evidence bundle** (reuse
   `evidencestore.Seal`, ADR-0029) and the attestation records in the **one audit stream** (action
   `access.recertify`, ADR-0034) — a SIEM-forwardable, tamper-evident sign-off. `lastAttested` is
   **derived from the audit stream** (`LatestAuditForObject`), so there is **no second table**. The
   attest refuses a stale grant-set hash (409 — attesting drifted state is a governance hole). Both the
   read and the attest are **View-grant-gated** (read = `reader`, attest = `runner`) like the audit
   stream, not ungated like the compliance score — a grant listing is a who-can-access-what disclosure
   (charter-guardian Flag 1, resolved in-slice).

## Charter posture

- **§1.1** every new Facet (`fileset.content`, `access.grants`) is demanded by a shipping Blueprint
  (the example CaC under `deploy/dev/examples/`), hash-pinned in `contracts/`, `additionalProperties:
  false`. No whole-Entity schema, no ontology creep.
- **§1.2** desired state is Git (Intent/Blueprint/Assignment); actual host state is a rebuildable
  projection via a read-only gather Run; drift is the diff. Recertification reads projected Facets +
  the audit stream — no second store. Not a writable CMDB.
- **§2.4** additive claim = the charter's literal `access` use-case; anti-GPO union verified
  structurally (compiled Baseline identity is per-Assignment, so contradictory claims both open
  Findings — no precedence field anywhere). `onRemove` semantics live in the schema/lifecycle.
- **§2.5** platform authz UNCHANGED (no OpenFGA/tuple writes in the diff); Access governs host access
  only. Secrets/keys never in the graph or audit detail (grant tuples + hashes are access metadata,
  not credentials).
- **§1.6/§1.8** recertification rides the one Evidence store + one audit stream + one authz model;
  overdue is surfaced, stale attestation refused; remediation/revoke are operator-launched refs.
- **§2 vocabulary** `Intent/FileSet`/`Intent/Access` exact; `subject` (host account) kept distinct
  from the Principal Kind; no banned `inventory`/`resource`/`CI` in identifiers.

## Reviews

- **charter-guardian: PASS**, two flags, both resolved in-slice: (1) the recertification GET read was
  ungated — now gated `reader`-on-View like the audit read; (2) the grant tuple field `principal`
  overloaded the frozen Principal Kind — renamed to `subject`.
- **vocabulary-linter: CLEAN** (kind spellings exact; "play/playbook" only in ansible tool content).
- **dependency-scout: RECOMMEND** (zero new dependencies — pure spine reuse).

## Alternatives considered

- **Intent/Access as platform RBAC-as-code.** Rejected: platform authz is already the CaC/OpenFGA
  spine (§2.5); an Intent minting platform grants is a second authz truth. The charter pins Access to
  host OS access (§2.4).
- **FileSet exclusive-per-file claim.** The per-namespace claim engine cannot express per-file
  exclusivity without an engine change; additive over the keyed Facet is charter-clean (union of
  distinct files; contradictory same-key claims both surface as Findings — no silent precedence) and
  avoids false compile conflicts across files on one host.
- **A recertification/attestation table.** Rejected: the audit stream is the durable attestation record
  (§1.6), so `lastAttested` is a query over it — no second store, no migration.
- **Content-addressed OCI now.** Deferred to the Bundle/cosign rails (ADR-0032); the source-ref +
  digest loop delivers the drift/remediation/Evidence value now (the in-tree-now/OCI-next path CIS took).
- **A host-gather Syncer for the collector.** Rejected: a parallel execution stack (§1.4); projection
  is a Run side-effect.

## Honest deferrals

- **FileSet:** content-addressed OCI artifacts + cosign verify (Bundle rails); recursive directory
  trees + purge-unmanaged; `validate=`/pre-commit hooks; SOPS value-level secrets; ring/canary rollout
  with auto-rollback; auto-deriving the collector's managed-file list from the declared Intents (v1
  takes it as a play var).
- **Access:** time-bound/JIT grants (Assignment TTL auto-expiry); SoD conflict checks as Findings;
  **overdue-recert as a first-class Finding** (v1 surfaces it on the report but does not open a
  View-scoped Finding — that needs a View-scoped, not Entity-scoped, Baseline); a unified
  "who-can-access-what" query spanning platform authz + host access; short-lived SSH-cert CA;
  Windows/AD accounts. PAM session brokering/recording is a Connector integration (Teleport/Boundary),
  never reimplemented.
- **Both:** the gather plays are structurally valid but the **live-host e2e is not run this slice**
  (the ADR-0033 precedent — probe details are operator-tunable); the store/compiler/evaluator/fold
  paths are proven by unit + real-DB integration tests. `onRemove: revert` lands here for these kinds.

## Consequences

The Intent capability board is nearly complete: four of five charter kinds ship. FileSet gives
declarative, content-addressed file distribution with continuous drift→Finding and gated remediation;
Access gives declarative host-access-as-code with an IGA recertification loop that produces
object-locked, audited Evidence — both capabilities AAP lacks, as open ones. No new engine, no new
dependency, no new graph write-path — new schemas, one collector-projection extension, and one
read/attest surface on the shipped Evidence + audit spine.
