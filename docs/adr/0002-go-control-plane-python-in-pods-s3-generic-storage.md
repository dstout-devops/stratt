# ADR 0002 — Go control plane; Python confined to pods & SDK; S3-generic storage

- **Status:** Accepted
- **Date:** 2026-07-10
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.4, §1.5, §1.7, §2.2, §3, §7.2
- **Amends:** charter §3 (the former "Backend: Python 3.12+ / FastAPI / Pydantic v2 / SQLAlchemy"
  commitment) and the §3 substrate line ("S3/MinIO").

## Context

The original backend-language choice (Python) was made when Stratt was essentially "just Ansible,"
and rested on two premises that the *locked* charter then dissolved:

1. **"Stay in-language to reuse the Ansible/Python runner tooling."** The charter demoted Ansible to
   **one Actuator among several**, executing as a **subprocess inside ephemeral pods**, and CI-bans
   `ansible.*` imports from the control plane for GPL hygiene (§3). The control plane never touches
   Python-Ansible; the `ansible-runner` shim lives in the EE image. The premise is gone.
2. **"Pydantic gives us native Contracts."** The charter made **Contracts *data*** — pinned,
   hash-verified JSON Schema documents, some derived from tofu plans or MCP declarations (§1.5,
   §2.2). They were never going to be Python classes. When Contracts are data, the backend only needs
   a good JSON Schema validator — which every language has.

What the charter's actual shape describes is a **K8s-native reconciliation platform**: sync
controller, dispatcher, compiler cadences, a graph-store frontend, an operator posture. That is Go's
deepest domain (client-go / controller-runtime), and the substrate we already chose — **NATS,
Temporal, OpenFGA** — is Go-native. The pull agent (`stratt-agent`) was already specced as Go, so
Python meant **two** languages; Go means **one**, with shared types across agent and control plane.

Separately, the substrate line named **MinIO** specifically. MinIO's single-vendor licensing posture
and 2025 community-edition feature-stripping make it exactly the single-vendor dependency §7.2 warns
against.

This is the last cheap moment to decide: pre-Phase-0 it is a charter edit; post-Phase-1 it is a
rewrite.

## Decision

1. **The control plane is Go.** Controllers, dispatcher, compiler, graph-store frontend, and API are
   Go. **API is OpenAPI-first** (huma / oapi-codegen); the `/api/v2` façade is REST regardless.
   Contracts and Facet schemas remain **data** (JSON Schema), validated by a standard validator —
   never modeled as the language's classes.
2. **Python is confined to (a) execution pods** — the `ansible-runner` shim and tool glue in EE
   images — **and (b) one supported plugin-SDK language**, so Ansible-community contributors are not
   excluded.
3. **Object storage is any S3-compatible store**, never a named vendor. Reference implementations:
   Garage, SeaweedFS, cloud S3. MinIO is removed from the charter by name.
4. **Kept as-is** (still fit, arguably better): Postgres, Temporal, NATS, OpenFGA, the React/TS
   frontend, Loki/OTel.

## Charter alignment

- **§7.2 (governance / community — the moat):** decisive. Language choice here is a
  **contributor-demographics** decision: the Argo / Crossplane / NATS maintainers a CNCF-track
  platform courts write Go. Also removes the MinIO single-vendor risk.
- **§1.4 boring spine:** Go + native SDKs for the chosen substrate is *more* boring than Python
  bridging to Go-native services.
- **§1.5 / §2.2 typed seams:** reinforces "Contracts are data," removing the Pydantic-class temptation.
- **§1.7 evergreen:** Go's compatibility promise and trivial toolchain bumps are best-in-class;
  single static binaries simplify deployment and the support matrix.
- **§3:** GPLv3 boundary becomes structural — the Go control plane *cannot* link Python-Ansible.
- No Founding Discipline (§1) or non-goal is altered; this is a §3 architecture-commitment amendment.

## Consequences

- **Positive:** one language across control plane + agent with shared types; native substrate SDKs;
  performance headroom for View queries / compile passes at 10⁶ Entities; cleaner GPL boundary;
  contributor pool aligned with the target community; vendor-neutral storage.
- **Negative / trade-offs:** gives up Python solo-founder velocity — mitigated because the steward
  ships Go daily (QuestForge is Go + gRPC + Postgres) and AI-assisted coding flattens the gap. Two
  ecosystems still exist at the SDK/pod boundary (by design, and isolated).
- **Follow-ups:** control-plane scaffolding (Phase 0) is Go modules, not a Python package; pick the
  Go migration/query tooling (e.g. `pgx` + goose/atlas or sqlc) in a later ADR; wire the CI evergreen
  gate for the Go toolchain (§1.7); ensure the plugin-SDK Contract surface stays JSON-Schema-first.
- **Repo config updated in lockstep:** `CLAUDE.md`, `.claude/rules/backend-go.md` (new),
  `.claude/rules/backend-python.md` (rescoped to pods/SDK), `.claude/rules/infra-supplychain.md`,
  `.claude/agents/dependency-scout.md`.

## Alternatives considered

- **Stay Python (FastAPI/Pydantic/SQLAlchemy).** Rejected: its two founding premises (Ansible
  in-language reuse; Pydantic-native Contracts) no longer exist under the locked charter, and it
  splits the codebase into two languages against a Go pull agent while drawing from the wrong
  contributor pool.
- **A third language (e.g. Rust) for the control plane.** Rejected: worse ecosystem fit for the
  K8s-operator/reconciliation domain and the chosen substrate's native SDKs; against boring-spine.
- **Keep MinIO by name.** Rejected: single-vendor governance risk (§7.2); S3 is the actual contract.
