# ADR 0051 — The EE Job speaks the port: a subprocess transport, one governor

- **Status:** **Accepted** (2026-07-17, steward) — extracts the flagship ansible Actuator's content-expertise
  out of the core while PRESERVING the charter §3 execution primitive (K8s Job in the EE image), by making the
  EE Job emit the sovereign port's typed shapes on stdout and governing them hub-side with the same
  `pluginhost.ApplyRaw` that governs the gRPC transport. One charter-guardian pass (SOUND-WITH-CHANGES →
  folded; see Reviews). Gates the ansible-over-the-port implementation.
- **Date:** 2026-07-17
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.4 (boring spine, pluggable everything — no `if ansible{}` in core), §1.5 (sovereign
  contracts, multiple transports), §1.2/§2.1 (single writer, ownership), §1.8 (never hide failure), §2.5
  (secrets brokered, never held), §3 (K8s Jobs are the only execution primitive; ansible subprocess-only in the
  EE image; ansible-builder EE compatibility), §7.3 (signing/trust tiers). Amends ADR-0032 (the dispatch fold
  moves hub-side); evolves ADR-0046 (content-blindness), ADR-0047 (§2 write path, §6 ItemResult), ADR-0049
  (agent-is-a-relay, extended to the EE-Job Site path), ADR-0048 (the license firewall).

## Context

opentofu and certissuer were extracted as long-lived gRPC plugins that run their tool IN-PROCESS. Ansible — the
flagship, the AAP-replacement raison d'être — cannot follow that shape: charter §3 mandates it runs as a **K8s
Job in the EE image** (ansible-builder compatibility = the day-one content ecosystem; per-run ephemeral pods =
multi-tenancy / network-policy / isolation; the GPLv3 boundary). Discarding that model for an in-process
long-lived ansible plugin would throw away the exact isolation and ecosystem ansible exists for.

Yet the substrate goal (ADR-0046) is content-blindness: an `if ansible {…}` in the core is a design failure,
and the ansible `Prepare`/`Interpret`/`ExtractFacts` expertise in `core/internal/actuators/ansible/ansible.go`
is precisely that domain logic. The extraction must remove it from Go **without** discarding the EE-Job model.

The steward's decision: **the EE Job speaks the port.** The Job stays a K8s Job in the EE image; an in-EE
`stratt-ansible` shim carries the content-expertise and emits the port's typed shapes on stdout; the core
becomes content-blind. A subprocess-emitting-typed-stdout is an explicitly blessed §1.5 transport — indeed the
*original* license-firewall case (ADR-0048) — so a second transport beside gRPC is charter-native, not novel.

## Decision

Ansible runs as a **K8s Job in the EE image** (execution primitive, EE image, GPLv3 boundary, kubelet credential
mounts — all UNCHANGED). The raw `ansible-runner` command is replaced by an in-EE **`stratt-ansible` shim** that
renders the inventory from the core-resolved targets, runs `ansible-runner -json`, and emits the sovereign
port's typed shapes (`TaskEvent` / per-host `ItemResult` / `ObservedEntity` write-back / `DiffFragment` drift,
proto-JSON) on stdout. The core dispatches the Job, decodes the typed lines, and governs them **hub-side** with
the same `pluginhost.ApplyRaw` that governs the gRPC transport. The ansible content-expertise leaves Go.

### The pinned invariant (highest-risk)

> The EE Job is the least-trusted zone; its typed stdout is ungoverned, provenance-free wire data. Exactly ONE
> governor exists — hub-side `ApplyRaw`, over the core-held resolved `ApplyTarget` set and the per-Step grant —
> which folds status, gates write-back (identity/facet/label), rejects out-of-set `ItemResult`s, and computes
> `Succeeded` from a required terminal. The dispatcher and any Site agent PARSE and RELAY typed lines but fold
> nothing, gate nothing, and stamp no provenance. A compromised EE Job (hub-local or at a Site) forges only
> within its per-Step grant + resolved-View scope, never estate-wide, and can never read as a green Run by
> dying silently. Two transports, one governance path — proven by `ApplyRaw` being transport-agnostic and
> `dispatch` retaining zero fold/gate logic.

### The must-fixes (folded from the guardian pass; these are the design, not options)

- **MF1 — Unify the governor (the fork hinge, §1.4/§1.8, ADR-0047 §6).** `ApplyRaw` becomes transport-agnostic
  (accepts a `Recv() (*ApplyResponse, error)` stream interface; both the gRPC client and a stdout→`ApplyResponse`
  adapter satisfy it). `dispatch`'s own per-target fold, status escalation, and `Succeeded` computation
  (`dispatch.go` followLogs) are **DELETED**, not run in parallel — parallel fold logic is two governance paths
  and is REWORK.
- **MF2 — the Site path obeys ADR-0049 (agent-is-a-relay, §1.2/§2.1/§1.8).** An EE Job at a Site: `dispatch`/
  `sitegw` forward the **raw** typed `ApplyResponse` bytes to the hub; the fold, gates, `Succeeded`, and
  provenance stamping run hub-side. No Site-side governance. (This directly reverses `dispatch.go`'s current
  Site-side fold — the exact Model-A trap ADR-0049 rejected.)
- **MF3 — bound the per-Step grant (§2.1, invariant #11, ADR-0047 §2).** The facet/label/identity grant handed
  to `ApplyRaw` for an ansible Step is core-resolved from the Baseline/Blueprint's *claimed* facets + the
  Actuator registration — never a wildcard, never pod-asserted. A general-purpose actuator with a wildcard
  facet grant neuters the write-back gates.
- **MF4 — the confused-deputy target set is core-held (§1.8, ADR-0047 §1.1/§6).** The resolved `ApplyTargets`
  are passed LEGIBLY to the shim (never baked into the opaque `desired`); the shim renders its inventory FROM
  them; `ApplyRaw` gates each `item_key` against the `RunAgainstView`-resolved set, never the pod's self-reported
  hosts. A playbook using `add_host`/dynamic inventory to report extra hosts is rejected.
- **MF5 — preserve the §1.8 diagnostic floor (invariant #12).** Three independent failure signals survive: (i)
  the K8s Job exit (`waitForJob`), (ii) `ApplyRaw`'s required-terminal (`Succeeded = sawTerminal && !failed` —
  a shim that dies without a terminal `ItemResult` folds to not-OK automatically), and (iii) the dispatcher's
  unclaimed-line diagnostic ring, re-keyed on "not decodable as `ApplyResponse`". The shim forwards
  `ansible-runner` banners / python tracebacks / stderr as typed diagnostic `TaskEvent`s — never swallowed.
- **MF6 — check-mode via the port `DryRun` bit (content-blindness).** The core drops the ansible-specific
  `params.check` field it sets today; a baseline drift check is `ApplyInvoke.DryRun`, which the shim maps to
  `--check --diff` — exactly as opentofu maps `dry_run → tofu plan`. The core setting `check` would be
  content-awareness.
- **MF7 — one credential authz path (§2.5/§1.6).** The CredentialRef use-check / use-without-read / audit /
  per-identity cost is a single hub-side chokepoint for BOTH transports; only the injection mechanism differs
  (kubelet Secret-mount for the Job, plugin broker for gRPC). This is textbook §1.5 "one contract, multiple
  transports" — and for a K8s Job the kubelet mount is *strictly less* material exposure (§2.5), not a
  regression.

### `dispatch` is the EE/subprocess-transport adapter

`dispatch` is no longer a generic Job dispatcher — it is the EE/subprocess-transport adapter. Residual EE-isms
(the `/runner` private-data-dir, the `runner` uid, `/runner/credentials/`) are pushed into JobSpec/transport
config so the core is not silently coupled to one EE shape (§1.4). The shim shells out to `ansible-runner`,
never links Ansible — the GPL boundary is the EE image (cleaner than today's in-Apache-core JSON parse), and
the EE/shim is signed + trust-tiered like any plugin (§7.3).

## Consequences

- **Positive:** the flagship extracts without discarding the charter §3 execution primitive (EE-Job isolation,
  ansible-builder ecosystem, GPLv3 boundary); the core becomes content-blind for ansible (`actuators/ansible`
  deleted); ONE governor (`ApplyRaw`) over TWO transports (gRPC + EE-Job-stdout); the kubelet credential model
  is preserved (no new broker); the pattern generalizes to a future WinRM/salt-run EE-Job actuator; and
  `dispatch`'s Site-side fold — the latent ADR-0049 Model-A trap — is removed, tightening the whole execution
  path.
- **Negative / cost:** MF1 is a real refactor (delete `dispatch`'s fold, thread a transport-agnostic stream
  into `ApplyRaw`); MF2 extends `siteproto`/`sitegw` to forward the typed stream Site→hub (the largest slice);
  a signed, trust-tiered shim deliberately lying (mapping a failed play to `STATUS_OK`) is the same residual
  trust surface as a lying gRPC plugin — bounded by §7.3 signing/tiers, not by the port (§1.8 protects only
  against *silent* death, which is covered).
- **Amends ADR-0032:** the per-target fold / `Succeeded` computation moves out of `dispatch` (Site-side today)
  into hub-side `ApplyRaw`.

## Alternatives considered

- **In-process long-lived ansible gRPC plugin** (the opentofu shape). Rejected: discards per-run K8s-Job
  isolation / multi-tenancy / network-policy and the ansible-builder EE ecosystem — the charter §3 model ansible
  exists for — for the one actuator where they matter most.
- **Keep ansible in-tree.** Rejected: the `Prepare`/`Interpret` domain logic in the Apache core is the
  `if ansible {…}` content-awareness ADR-0046 forbids; extraction is the point.
- **The core interprets the Job's raw ansible `-json` (status quo dispatch).** Rejected: that keeps ansible
  semantics in the core (`HostStatus`, `ExtractFacts`) and is not content-blind.

## Reviews

- **charter-guardian, pass 1 — the EE-Job transport (2026-07-17): SOUND-WITH-CHANGES → folded.** The design is
  a charter-honoring reconciliation of §3 with content-blindness (a subprocess transport is §1.5-native, the
  original license-firewall case), but "one governance path, two transports" was aspirational: `dispatch.go`
  today folds status + computes `Succeeded` Site-side — the ADR-0049 Model-A trap. Seven must-fixes make it
  structural: MF1 (unify the governor; delete dispatch's fold), MF2 (Site path relays raw, governs hub-side),
  MF3 (bound the per-Step grant), MF4 (core-held confused-deputy target set), MF5 (three-signal §1.8 floor),
  MF6 (check via port DryRun), MF7 (one credential authz path). Confirmed clean: content-blindness holds (typed
  decode ≠ interpretation; `ItemResult_Status` is the port's own vocabulary), the two credential injections are
  one §2.5 contract, and no permanent non-goal or banned identifier is breached.

- **charter-guardian, pass 2 — the Phase 5a implementation (2026-07-17): SOUND-WITH-CHANGES → folded.** The
  hub-local integration realizes "one governor, two transports": `dispatch.RunStream`/`followTyped` decode +
  publish + relay, folding/gating nothing; `pluginhost.GovernStream` is the sole governor over both the gRPC and
  the EE-Job channel-stream; the confused-deputy `resolved` set is core-held (MF1/MF4). The synthesized
  `{"host.name": name}` is a per-run correlation LABEL, not a second identity truth — facts flow only through the
  `res.Facts` (name→EntityID) channel via the core-held `resolved.Targets`, never an `UpsertEntities`-by-host.name
  path (§1.2 upheld). Only CredentialRef **names** cross into the Job content (§2.5). One CHANGES-REQUIRED, folded:
  **C1** — the K8s Job exit is a distinct MF5 signal, so `Succeeded = raw.Succeeded && jobOK` (a green terminal
  followed by a non-zero exit reads NOT-OK, parity with the in-tree floor). Tracked follow-ups: **F1** — Phase 5b
  must DELETE the in-tree `actuators/ansible` content-awareness (Phase 5a env-gates it as the pre-cutover
  fallback; content-blindness is not *achieved* until the deletion lands, only *reachable*); **F2** — the MF3
  grant is registration-static (matches the gRPC `PluginActuator` posture, not a regression); the tighter
  per-Step *claimed-facet intersection* MF3 describes is a later refinement.

## Phase 5b — the cutover (2026-07-17)

The env gate is gone: ansible is now EXCLUSIVELY the EE-Job transport. `core/internal/actuators/ansible`
(the `Prepare`/`Interpret`/`BuildContent`/`HostStatus`/`ExtractFacts`/`ExtractDiff` content-awareness) is
**deleted** — the Apache spine no longer holds an `if ansible {…}` (§1.4, guardian **F1** resolved). `strattd`
registers "ansible" only as the EE-Job `PluginActuator`; `stratt-agent`'s in-tree Interpreter registry drops
ansible (it was dead for ansible the moment the hub routed ansible through `GovernStream` — a `PluginActuator`
never takes the Site fold path). The `actuators/ansible.input` **Contract stays** (pinned schema data, §1.5);
the flagship's expertise lives in the `plugins/ansible` shim module.

**Regression window (intended by the roadmap's hub-local-first sequencing):** a **Site-homed** ansible Run now
fails closed at the hub (`JobTransportSiteUnsupported`) until the EE-Job-at-a-Site path lands (Phase 6, MF2).
Hub-local ansible is unaffected.

**Outstanding verification (the one signal this environment cannot produce):** a live **dev-cluster** end-to-end
ansible Run (real EE pod, NATS, Temporal). Everything else is green — `go build`/`vet`/`gofmt`, the
pluginhost/dispatch/orchestrate/strattd unit suites, and a full `docker build` of the EE image whose baked
`stratt-ansible` binary emitted the exact port shapes against a stubbed runner. Run the dev-cluster Run before
relying on the cutover in a real deployment; there is no in-tree fallback behind it now.
