# ADR 0007 — Phase-0 Syncer SDK and dev/test harness: govmomi + vcsim, kind

- **Status:** Accepted
- **Date:** 2026-07-11
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.4, §1.7, §2.2, §3, §7.1, §8

## Context

The Phase-0 spike (§8) needs (a) one native vCenter-class Syncer and (b) a Kubernetes
cluster to develop and test the dispatcher, since K8s Jobs are the only execution
primitive (§3). The devcontainer has docker-in-docker. dependency-scout evaluated both
choices (2026-07-11).

**govmomi** (vSphere Go SDK, bundles the vcsim simulator): Apache-2.0, DCO; active
through the Broadcom era (releases monthly-to-quarterly, commits current); the de facto
standard already inside the OpenTofu vSphere provider, Packer, and cluster-API. Cautions:
never reached v1.0 — minor bumps occasionally break (treat every minor as potentially
breaking); single-lead-maintainer concentration (acceptable for a vendor's own SDK to its
own API); vcsim has documented fidelity gaps in exactly the delta-ingestion path
(`WaitForUpdatesEx`/`CheckForUpdates` semantics, issues #922/#2153).

**Transport note for charter-guardian:** vSphere's change-feed (PropertyCollector /
`WaitForUpdatesEx`) exists only on the SOAP vim25 API — the REST/vAPI surface has no
equivalent. §2.2's "full-fidelity transports (native REST/gRPC)" is read as "the vendor's
own native transport, no third-party abstraction," which SOAP vim25 satisfies; it is the
*only* native transport with delta semantics vCenter offers.

**kind** (Kubernetes SIGs): the upstream project's own e2e tool; near-continuous tracking
of K8s minors; OpenSSF Maintained 10/10; runs unmodified kubeadm-assembled upstream K8s —
the reference "vanilla" target for §7.1 any-Kubernetes neutrality. k3d rejected: default
K8s version sat at 1.21 (EOL) until mid-2026, >15-month release gap, single-maintainer,
and k3s's deliberate deviations from vanilla K8s risk baking non-portable dispatcher
behavior.

## Decision

1. **The vCenter Connector's Syncer is built on govmomi, pinned v0.55.1**, using the
   SOAP vim25 PropertyCollector for bulk enumeration and delta ingestion (the top-rung,
   hand-written Contract path required for Syncers, §2.2).
2. **vcsim (same module version; container `vmware/vcsim:v0.55.1`) is the dev/CI target**
   for the Syncer — a development accelerant, **not** the integration oracle for the
   delta path: a periodic test against a real vCenter/ESXi target is a follow-up before
   the delta path is declared production-grade.
3. **kind, pinned v0.32.0, is the dev/CI Kubernetes** for the dispatcher's Job-execution
   tests. Dev/test-harness dependency only; nothing in the dispatcher may assume kind.

## Charter alignment

- **§2.2:** native full-fidelity transport for the Syncer (see transport note); the
  Syncer's Contract is hand-written (top rung only).
- **§1.4 / §7.1:** both are the boring, ecosystem-default choices; kind keeps the
  dispatcher honest against vanilla upstream K8s.
- **§1.7:** pins ride the quarterly train; govmomi minors are treated as potentially
  breaking (test N and N-1 in CI); kind CI matrix covers node-image N and N-1 to prove
  client-go skew tolerance.
- No Founding Discipline or non-goal is touched.

## Consequences

- **Positive:** the Phase-0 go/no-go gates (projection freshness, View query at scale)
  are exercisable entirely inside the devcontainer; the Syncer uses the same library the
  rest of the vSphere ecosystem trusts.
- **Negative / trade-offs:** vcsim-only CI could pass while real delta ingestion
  degrades — named, accepted for the spike, and gated before production; govmomi's
  pre-1.0 semver requires minor-bump vigilance.
- **Follow-ups:** quarterly live-vCenter smoke test for the PropertyCollector delta path;
  Renovate/Dependabot on every govmomi minor with N-1 compatibility gate; pin
  `kindest/node` images by digest in CI; track vSphere major support (8.0 EOGS Oct 2027)
  in the support matrix.

## Alternatives considered

- **pyvmomi** — rejected: Python is confined to execution pods and the plugin SDK
  (ADR-0002); Syncers are control-plane Go.
- **Hand-written SOAP client** — rejected: reimplements govmomi without its maturity
  (§1.4).
- **k3d** — rejected: evergreen violations (stale default K8s, stalled cadence),
  single-maintainer risk, and k3s's non-vanilla deviations against §7.1.
- **Real vCenter lab as the dev target** — rejected for the spike: not reproducible in
  CI or the devcontainer; retained as the periodic integration target (follow-up).
