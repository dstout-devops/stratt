# ADR 0013 — Helm packaging: any-Kubernetes self-host

- **Status:** Accepted
- **Date:** 2026-07-12
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.4, §2.5, §7.1 (any-K8s self-host), §7.3 (supply chain), §8 (Phase 1 "Helm chart")

## Context

Last Phase-1 infrastructure item. The chart packages the proven spine and lands the
follow-ups parked for it: the OpenFGA **postgres-engine + migrate** runbook (ADR-0009),
**digest-pinning** posture (§7.3), **UI bundling** (ADR-0012), and the slice-5 guardian's
**audience guard**.

## Decision

1. **BYO substrate.** `deploy/charts/stratt` deploys **strattd (+ bundled UI)** and,
   optionally, **OpenFGA as a subchart**. Postgres, NATS, Temporal, and the OIDC issuer
   are endpoints in values — their official charts are the boring path (§1.4); packaging
   them ourselves would mean maintaining forks of other people's operations. §7.1 is
   satisfied by the chart being plain-Kubernetes (no vendor API, RBAC v1, standard
   probes/Ingress).
2. **strattd image** (`deploy/docker/strattd.Dockerfile`): multi-stage node→go→
   `distroless/static:nonroot`, static binary, UI at `/ui` via `STRATT_UI_DIR` (same
   operational result as go:embed with zero code — supersedes ADR-0012's embed note).
   `USER 65532:65532` — **numeric**, because kubelet cannot verify `runAsNonRoot`
   against a symbolic user (found by the kind e2e). No secret material in any layer.
3. **Audience guard** (strattd): `STRATT_OIDC_ISSUER` without `STRATT_OIDC_AUDIENCE`
   refuses to boot unless `STRATT_OIDC_ALLOW_NO_AUDIENCE=true` — a loud, explicit
   dev-only opt-out (the dev harness sets it), never a default. `/healthz` added for
   probes: process-liveness only, no store/authz dependency.
4. **CaC delivery = git-sync sidecar** (registry.k8s.io/git-sync v4): syncs the
   declarations repo into an emptyDir; strattd reconciles the synced directory in
   plain-dir mode every interval — no git binary in the strattd image, atomic symlink
   flips, creds via an existing Secret. (git-sync needs a writable `/tmp` emptyDir
   under the read-only root — found by the kind e2e.)
5. **OpenFGA subchart**: official `openfga` chart **0.3.10** (dependency-scout
   RECOMMEND), `datastore.engine=postgres` with **DSN via `existingSecret` only**
   (never `datastore.uri` — §2.5), `applyMigrations: job` so `openfga migrate` runs as
   a hook before the server rolls; **server image pinned back to the ADR-0009 vetted
   v1.17.0** (the chart's default appVersion may run ahead of the scouted pin); the
   chart's bundled Bitnami DB subcharts stay off (scout: legacy-image governance risk).
   **Runbook:** memory engine = dev compose; postgres + migrate = production; tuples
   remain a projection of Git either way (rebuildable, ADR-0009).
6. **Secrets (§2.5):** every credential-bearing value has an `existingSecret` form;
   inline DSNs are dev conveniences that NOTES.txt warns about. The vCenter Source
   reads username/password from a Secret.
7. **Digest pinning (§7.3):** every image value takes `digest:` (wins over `tag:`);
   NOTES.txt warns per floating-tag image. CI enforcement (fail on tag in production
   values) is a follow-up gate, with cosign/SBOM/SLSA at first tagged release.
8. **RBAC:** namespace Role exactly matching the dispatcher — jobs create/get,
   configmaps create/delete, pods get/list/watch, pods/log get. Nothing cluster-scoped.
9. **replicas: 1** for now: the reconcile loops (desired-state, trigger, tuple sync)
   assume a single writer. Multi-replica needs leader election — recorded follow-up.

## Consequences

- Verified by a **real install into the dev kind cluster** (compose substrate via the
  kind network host gateway): OpenFGA migrate Job → server up; strattd Ready on
  /healthz; UI + API served; declarations arrived via git-sync (first cycle before the
  initial clone fails safe and skips); a Run started against the in-cluster API
  dispatched and completed an EE Job under the chart's ServiceAccount, with the
  `use`-check answered by the subchart OpenFGA from git-synced tuples; the audience
  guard crashed a mis-configured pod with an actionable error and booted with the
  explicit opt-out.
- Dev harness/compose users must now set `STRATT_OIDC_ALLOW_NO_AUDIENCE=true` (the
  guard is deliberately breaking for issuer-without-audience configs).
- Follow-ups: leader election → replicas>1; CI gates (digest-pin check on production
  values, chart N-1 upgrade smoke, assert Bitnami subcharts stay disabled and
  `datastore.uri` never set); NetworkPolicy + Kyverno policy set (§7.3, Phase-2/3);
  TLS/ingress hardening guidance incl. the ADR-0012 sessionStorage-token flag; cosign
  signing at first release; UI OIDC build-args story for downstream rebuilds.
- charter-guardian §7.3 flags (non-blocking, fold into the digest-pin CI gate): the
  OpenFGA server is pinned by tag not digest; the subchart's internal migrate-wait
  image (`groundnuty/k8s-wait-for`) is inherited unpinned; `values-dev-kind.yaml`
  carries the throwaway compose dev DSN literal (header forbids deriving production
  values from it).
