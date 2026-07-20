# ADR 0025 — AWX importer + ansible SCM content-ref

- **Status:** Superseded-in-part by [ADR-0086](0086-adopt-per-object-in-place.md) — Decision #1 (the
  one-shot `stratt import awx` verb + full-estate self-read) is retired and replaced by per-object
  `adopt`; Decision #2's transform *input* re-homes to a targeted per-object read. Decision #2 mapping
  logic, #3 (SCM content-ref `ansible.input.v3`), and #4 (clone-in-EE) **remain Accepted and live** —
  this ADR stays the authority for them. See the 2026-07-19 amendment below.
- **Date:** 2026-07-13
- **Deciders:** Project steward (dstout)
- **Charter sections:** §5.6 (AWX exodus, Flow 6), §1.2 (projections / no
  writable CMDB), §1.4/§3 (GPL boundary, boring spine), §1.5 (contracts are
  data), §1.8 (never hide diagnosis), §2 (vocabulary), §2.5 (secrets), §8
  (Phase-2 promote enabler); ADR-0022 (rung-2 derived Contracts)

> **Amendment (2026-07-19) — the verb is `adopt`, not `import`; the one-shot model is
> superseded.** This ADR's premise is a one-shot **`stratt import awx`** that reads an AWX
> and writes a CaC bundle. The AWX/ansible Connector arc that followed (the always-on
> Syncers projecting `ansible.*`, ADR-0085) obsoletes that framing: **we never import — the
> projection is continuous, we are connected and simply know the estate.** The graph already
> holds every AWX/ansible object; there is nothing to bulk-read. The deliberate act is
> therefore **`stratt adopt`** — taking authority over an *already-observed* object, flipping
> it in place from AWX-executed (read-only projection) to Stratt-executed (a Named Kind in
> Git). "Import" is the legacy-tool verb (AWX "imports inventory") and carries the one-shot
> mental model we reject. Wherever this document says import/importer/imported below, read
> **adopt**. The SCM content-ref decision (a Step referencing playbook content by project+path)
> stands unchanged — an adopted job template still needs its playbook. A future ADR will
> record the adopt design proper (per-object, in-place, over the live projection).

## Context

The charter's Phase-2 **promote enabler** is "AWX importer + `/api/v2` façade …
one team migrated green 2 wks" (§8; Flow 6). It is explicitly multi-slice.
**This slice ships the importer only** — the compat façade is a later slice.

Flow 6 calls the importer "an AWX Syncer + transform," but its *outputs* are
Git-declared **desired state** (Views / Workflows / CredentialRefs), not the
projection graph (§1.2). So it is a hybrid: it reuses the Connector *read*
scaffolding (the msgraph REST+pagination pattern) with a **CaC-emitting** write
side. It is a one-shot `stratt import awx` CLI verb that reads an AWX 24.6.1
`/api/v2` and writes a reviewable **bundle to a directory** (never the DB, never
the platform API); the operator reviews `migration-report.md`, then runs the
existing `stratt plan -d <bundle>`. Import target frozen at 24.6.1 forever.

An AWX job template references its playbook by SCM *project + path* — AWX's API
does **not** contain the playbook YAML (it lives in the customer's Git repo).
The ansible actuator accepted only inline `play`. So this slice also adds an
**SCM content-ref** to the ansible actuator (charter §5.6 "project (SCM) → tool
content ref on a Step") so imported job templates are runnable.

## Decision

1. **Read side — `connectors/awx/` (not a Syncer).** A read-only `/api/v2`
   client (token auth, `{count,next,previous,results}` pagination, an in-repo
   `awxsim` fixture). Package doc forbids wiring it into the Syncer registry: it
   never projects Entities. AWX nouns are vendor JSON tags / endpoint strings /
   decode-struct names (§2 latitude), never emitted Stratt identifiers.
2. **Transform — `awximport/` → a reviewable CaC bundle.** Pure (I/O only in
   `write.go`); every emitted document round-trips through
   `desiredstate.ParseDir` + `Validate*` (a load-bearing test). Mappings:
   - **job_template → single-Step Workflow** (the actuation tuple: ansible +
     scm content-ref + viewName + credentialRefs). No standalone "Step preset"
     type exists; a launchable single-Step Workflow is the carrier.
   - **workflow_job_template + nodes → multi-Step Workflow.** Node
     success/failure/always edges → Step `needs` + `when`; mixed-condition
     fan-in → one Step copy per condition; **approval node → Gate** (placeholder
     `approvers` + a blocking report entry — AWX carries no approver identity).
   - **inventory → View** (the crux, §1.2). *Dynamic* (aws_ec2/vmware/azure_rm)
     inventories are what native Syncers already do → emit a View selecting over
     the **native Syncer's projected labels** (aws_ec2 → `kinds:[instance]`,
     scope on `aws.region`) and recommend the native Connector; never re-import
     hosts. *Smart* (`host_filter`) → reduce simple `and`-conjunctions of
     equality into selector predicates; drop and **report** the irreducible
     remainder (or/not/regex/non-exact operators). *Static* (manual hosts) — the
     writable-CMDB anti-pattern → emit a compat-label selector
     (`awx.inventory.name`) + a **blocking** report entry; the hosts are
     **never** projected as Entities.
   - **survey → input Contract** — a rung-2 JSON Schema doc (question types →
     `properties`, `required`/`default`/bounds, `password` → `x-stratt-sensitive`,
     multiplechoice → `enum`).
   - **credential → CredentialRef** — pointer + injection policy only; **material
     is never imported** (§2.5; AWX returns `$encrypted$` anyway); `ownerTeam`
     and `locator` are `REVIEW-ME` re-broker placeholders.
   - **migration-report.md** in Stratt vocabulary (AWX terms only in a "was:"
     compat column); a **blocking-items** checklist to resolve before apply.
3. **ansible SCM content-ref (`ansible.input.v3`).** Adds `scm{repo,ref?,
   playbook}`, `play` XOR `scm` (`"not":{"required":["play","scm"]}`). The v3
   sibling wins the `actuators/ansible.input` lookup by path-sort load. `Prepare`
   with `scm` emits a **static** `clone.sh` + `Env{SCM_REPO,SCM_REF,SCM_PLAYBOOK,
   SCM_CHECK}` + `Command:["sh","/runner/clone.sh"]` and still renders
   `inventory/hosts` from the View (the View stays the truth). `validateSCM`
   rejects empty repo/playbook and path traversal.
4. **Clone runs in the EE pod (§1.4/§3, §2.5).** git is installed in the EE
   image and exec'd there; the control plane emits only strings (no `go-git`,
   consistent with the exec'd-git posture in `desiredstate/controller.go`).
   Untrusted values arrive as **env**, used quoted in the script — never
   interpolated into the command line. A private-repo credential reaches the pod
   solely via the existing CredentialRef file-injection.

## Consequences

- **Live-verified (dev harness):** `stratt import awx` against `awxsim` → a
  9-file bundle + report → `stratt plan` against a live `strattd` plans every
  declaration clean (creates, no validation errors — the scm Steps validate
  against the v3 Contract through the API). The dynamic-inventory View
  `awx/cloud-ec2` resolved to **2 real `instance` Entities** from the live
  awsec2 sync; the static-inventory View `awx/legacy-prod` resolved to **0**
  (no hand-entered hosts projected — §1.2 holds).
- **Deferred (flagged, not faked):**
  - **Survey enforcement.** `types.Step` cannot yet reference an arbitrary input
    Contract, so the survey Contract is emitted + reviewable but **not enforced**
    against launch params. The Step-level `inputContract` binding is a named
    follow-up; `survey.go` + the report say so plainly.
  - **Private-repo SCM clone e2e.** The actuator mechanism + a public/pre-seeded
    clone are in; the private-repo credential-helper wiring (`Prepare` learning
    the injected key path) and a git-serving harness fixture for the full
    private-clone Run are a fast-follow.
  - **Parametrized inventory filters** beyond `and`-of-equality; the report
    names every dropped term.
- **The one departure recorded:** a manual-project job template (its playbook
  content is not in AWX's API) emits a **placeholder `play`** (valid, harmless,
  round-tripping) plus a blocking report entry, rather than an invalid empty
  scm — the bundle stays parseable while the gap stays visible (§1.8).
- The `awx.` compat labels (`awx.inventory.name`, `awx.group.name`,
  `awx.host.name`) are vendor-prefixed **label keys** — projected data used for
  matching, not Stratt core-model identifiers — so the banned-noun rule (which
  governs type names / tables / API paths / CLI nouns / Facet namespaces) does
  not reach them.

## Runway after
`/api/v2` compat façade (auth→Principal mapping, `{count,next,results}` envelope
+ page/page_size/filters, run→job status, SSE→stdout adapter); Step
`inputContract` binding (enforce imported surveys); private-repo SCM
credential-helper + git-server harness e2e; notifications; Intent kinds GA +
object-locked Evidence (Phase 3).
