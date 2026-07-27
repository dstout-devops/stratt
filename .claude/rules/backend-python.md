---
paths:
  - "**/*.py"
  - "**/pyproject.toml"
---

# Python rules — execution pods only — charter §3

**Python is not the control plane** (that is Go — ADR-0002, `.claude/rules/backend-go.md`) and it is
**not the plugin SDK** (that is Go too — ADR-0141). In Stratt, Python lives in exactly ONE place; if
you are writing Python for anything else, stop and reconsider — it belongs in Go.

1. **Inside execution pods (EE images):** the `ansible-runner` shim and tool-content glue that runs
   in ephemeral K8s Job pods. This is the GPLv3 subprocess boundary (§3) — it runs Ansible; it is a
   *separate process in a separate image* from the control plane, never linked into it.

That is the whole list. The second entry used to be "the plugin SDK: one supported language for
community Connector/Actuator authors" — a commitment made when the backend itself was Python
(FastAPI/Django), carried forward unexamined when ADR-0002 moved the control plane to Go, and never
built. ADR-0141 retired it: the SDK is Go, and the PORT is the contract, so a plugin author is free
in any language with a protobuf toolchain rather than confined to one Stratt chose for them.

An Ansible contributor is still not excluded, which is what the old clause was protecting — their
contribution is tool CONTENT (playbooks, roles, collections) in the ansible plugin's `contentDir`
tree, never a Connector implementation.

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
