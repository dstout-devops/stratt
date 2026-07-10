---
paths:
  - "**/*.go"
  - "**/go.mod"
  - "**/go.sum"
---

# Control plane (Go) rules — charter §3

The **control plane is Go** (ADR-0002): reconciliation controllers (sync controller, dispatcher,
compiler cadences), the graph-store frontend, the API, and the `stratt-agent` pull agent. This is the
K8s-native-operator domain; lean into that ecosystem.

- **Ecosystem:** prefer `controller-runtime` / `client-go` patterns for reconcilers; use the
  **native SDKs** for substrate — NATS, Temporal (`go.temporal.io/sdk`), OpenFGA. Postgres via
  `pgx`. These being Go-native is a large part of why the control plane is Go — don't reach for a
  thin wrapper over a foreign-language service when a native SDK exists.
- **API is OpenAPI-first** (§3): generate from a spec with `huma` or `oapi-codegen`; the `/api/v2`
  façade is REST and curl-ability matters for adoption. Don't hand-roll handlers that drift from the
  published schema.
- **Contracts & Facet schemas are data, not Go types** (§1.5, §2.2): they are pinned, hash-verified
  **JSON Schema documents** (some tool-derived from tofu plans / MCP declarations). Validate with a
  standard JSON Schema validator; do not model a Contract as a Go struct and call that the contract.
  Go structs are an internal convenience, never the source of truth for a Contract.
- **Shared types with the pull agent:** control plane and `stratt-agent` are one language — factor
  shared wire/domain types into a common module rather than duplicating them.
- **Projections, never a second truth (§1.2):** only Normalizer and Run-provenance code paths may
  write Entity/Facet/Relation attributes. Make that a structural property of the write layer
  (constrained repositories / ownership checks), not a review norm. Every attribute write stamps
  Provenance.
- **No implicit precedence anywhere (§2.4):** claim resolution is exclusive-fails-compile or
  additive-union. Never add a priority/last-writer-wins field.
- **Evergreen (§1.7):** Go's compatibility promise is a feature — stay current on the Go toolchain
  (N-1) and keep deps updatable; single static binaries are the deployment target. Wire the CI
  evergreen gate.
- **GPL hygiene (§3):** never import or vendor Ansible or other GPL code into the control plane; it
  shells out to `ansible-runner` in the EE image. Keep the module graph clean of copyleft.
- **Secrets never persist (§2.5):** resolve `CredentialRef`s to material only at pod spawn; never
  log or write secret material to the graph or artifacts.
