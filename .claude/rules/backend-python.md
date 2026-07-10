---
paths:
  - "**/*.py"
  - "**/pyproject.toml"
---

# Python rules — execution pods & plugin SDK only — charter §3

**Python is not the control plane** (that is Go — ADR-0002, `.claude/rules/backend-go.md`). In Stratt
Python lives in exactly two places; if you're writing Python for anything else, stop and reconsider —
it probably belongs in the Go control plane.

1. **Inside execution pods (EE images):** the `ansible-runner` shim and tool-content glue that runs
   in ephemeral K8s Job pods. This is the GPLv3 subprocess boundary (§3) — it runs Ansible; it is a
   *separate process in a separate image* from the control plane, never linked into it.
2. **The plugin SDK:** one supported language for community Connector/Actuator authors, so the
   Ansible-community ecosystem is not excluded. Keep this SDK's surface small and typed.

Rules for that Python:
- **Env & deps:** `uv` only (`uv add`, `uv sync`, `uv run`). Pin versions; evergreen ≥ N-1 (§1.7).
- **Full type hints**, `ruff` for lint+format, `mypy`/`pyright` clean.
- **Contracts are data, not Python classes (§1.5, §2.2):** a plugin declares its input/output
  Contract as pinned JSON Schema; the control plane hash-verifies it. Pydantic is fine as an
  *internal* convenience inside the SDK, but the Contract of record is the JSON Schema document, not
  a Pydantic model. Do not reintroduce "Pydantic-native Contracts" — that was the pre-Go framing.
- **Secrets never persist (§2.5):** material is injected into the pod at spawn; never log, cache, or
  write it back to the graph or artifacts.
- **Provenance (§1.2):** anything a pod projects back to the graph flows through a Normalizer and
  carries Run provenance — pods do not write Entity attributes directly.
