---
paths:
  - "**/Dockerfile"
  - "**/*.Dockerfile"
  - ".github/workflows/**"
  - "**/charts/**"
  - "**/deploy/**"
  - "**/kustomization.yaml"
---

# Infra, CI & supply-chain rules — charter §1.7, §3, §7.3

- **Any-Kubernetes self-host is the differentiator (§7.1).** No OpenShift-only or vendor-only paths.
  **K8s Jobs are the only execution primitive (§3):** ephemeral, network-policied, secret-injected
  pods; org-namespace multi-tenancy; ship a Kyverno policy set.
- **Supply chain from release one (§7.3):** cosign-signed releases, SBOM, SLSA provenance,
  **pinned-digest images** (never floating tags in production manifests), community-tier plugins
  sandboxed by default. MCP outputs screened for tool-description injection wherever LLM-adjacent.
- **Evergreen contract is CI-enforced (§1.7), not aspirational.** Add policy gates that **fail the
  build** when a runtime/toolchain/substrate dep falls below N-1 on its major/LTS line. Applies
  bidirectionally: what Stratt runs on (Go, Node LTS, Postgres, Temporal, NATS — plus Python in
  execution pods / plugin SDK) and what Stratt supports (K8s upstream N-2 skew, Postgres N-1,
  actuator tool versions). Quarterly upgrade train is
  a release-blocking checklist item.
- **Secrets are brokered, never baked (§2.5):** no secret material in images, layers, or manifests;
  inject via CredentialRef at pod spawn only. `.env`/secret files must never be COPYed into an image.
- **Logs → Loki, artifacts/facts → S3, Postgres stores summaries only** (§3) — never replicate AWX's
  job-events-table pathology.
- **DCO enforced in CI** (§1.3) — every commit signed off; no CLA bot.
