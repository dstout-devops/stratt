# ADR 0074 — External policy engines (OPA / Kyverno) over the subprocess transport

- **Status:** Accepted
- **Date:** 2026-07-18
- **Deciders:** steward (dstout)
- **Charter sections:** §1.4, §1.5, §3, ADR-0046
- **Implements:** ADR-0061 §7.5 · builds on ADR-0072 (the PDP port), ADR-0073 (admission)

## Context

ADR-0072 made the PDP a swappable port (`policy.Decider`) with a built-in CEL provider and named "an external engine over the sovereign port" as the way to run OPA/Cerbos/Cedar/Kyverno-JSON. This ADR ships the first real external provider — proving the port is satisfiable by an out-of-process engine, so policy is genuinely *external like infrastructure*, swappable and bypassable.

**Transport choice — subprocess, not the plugin gRPC port.** The sovereign plugin port's `Invoke` verb carries **Action semantics**: the host projects an Invoke's result back into the graph as Entities/Relations (`ProjectFacts`). A policy *decision* is not an entity projection — routing it through `Invoke` would drag Action-projection meaning that does not fit. The charter (§1.5) sanctions **subprocess** as a first-class transport beneath the sovereign contract, exactly as **Ansible** (a core infrastructure tool) integrates. So the first external policy provider runs the engine as a **subprocess speaking the Decision contract over stdin/stdout** — the fastest correct path, and the same discipline the charter already blesses for tools. (gRPC and REST remain valid transports for the same port — a future provider.)

## Decision

**1. `policy.Exec` is an external-engine provider over the subprocess transport.** It implements the `Decider` port by running an external policy tool: the request is marshalled to JSON on **stdin**, and the tool returns a `types.Decision` JSON on **stdout**. The engine (OPA, Kyverno-JSON, anything) speaks the Decision contract — the tool, or a thin wrapper around it, emits `{outcome, reasons, obligations}`. `Exec` is engine-agnostic; the command is configuration.

```go
type Exec struct { Run func(ctx, op string, request []byte) ([]byte, error) }  // op: "decide" | "admit"
```

**2. Fail-closed, always.** A non-zero exit / transport error, unparseable output, or an unrecognised `outcome` **denies** (stamped with the reason and `engine`), never a silent allow (§1.8). The provenance records `engine=exec:<tool>` so the audit stream shows which engine decided. `Validate` delegates to the engine (the external engine validates its own policy dialect; the `Exec` provider does not re-validate inline CEL controls — those are the built-in CEL provider's dialect).

**3. Selected by configuration, still bypassable.** `main.go` wires the Decider: `STRATT_POLICY_EXEC_CMD` set ⇒ the `Exec` provider (subprocess to OPA/Kyverno); else the built-in `CEL` provider; `STRATT_POLICY_BYPASS` ⇒ `Bypass`. One line swaps the engine; bypass remains a deliberate, recorded configuration. The core call site is unchanged and content-blind — it sends the request through the port and acts on the `Decision`.

**4. The engine contract (documented, not core).** OPA: a Rego package emits the Decision (`opa eval` over a bundle, reshaped to `{outcome, reasons}`); Kyverno-JSON: a wrapper maps its pass/fail to allow/deny. An example OPA wrapper + Rego ships under `deploy/policy/` as reference. The Rego/Kyverno itself is the operator's policy — governance domain content living entirely outside the spine (§1.1/§1.4).

## Charter alignment

Upholds §1.5 (subprocess is a sanctioned transport beneath the sovereign Decision contract; the external engine is never load-bearing — the core runs on CEL or Bypass without it), §1.4/ADR-0046 (the engine and all its policy content live out-of-process; the spine stays content-blind, the call site unchanged), and §3 (OPA/Kyverno realise the "Kyverno-for-config" admission engine the charter named). No new dependency in core (subprocess exec is stdlib); no new Named Kind.

## Consequences

- **Positive:** a real external engine (OPA/Kyverno) runs governance over the port, out-of-process, swappable by one env var and bypassable — the "external like infrastructure" tenet realised in *deployment*, not just architecture; both Decide (gate PEP) and Admit (admission PEP) route to the external engine; the built-in CEL provider remains the zero-dependency default.
- **Negative / trade-offs:** a subprocess per decision is slower than in-process CEL (fine for admission at load and acceptable for run-time gates; a long-lived REST/gRPC provider is the perf follow-up); the engine's Decision-contract wrapper (Rego/Kyverno mapping) is the operator's responsibility, documented via the shipped example, not enforced by core.
- **Follow-ups:** a long-lived **REST provider** (OPA server) and/or a **gRPC** policy provider for lower per-decision latency; ship the OPA/Kyverno tool in the EE image; a port `ValidateAdmission`/`Validate` that delegates to the external engine's own policy linter.

## Alternatives considered

- **Route policy through the plugin gRPC port's `Invoke`** — rejected: `Invoke` is Action-semantic (results project into the graph); a decision is not a projection. A dedicated gRPC `PolicyService` is viable but heavier (proto + plugin module + deployment) — deferred; subprocess delivers a real engine now over a sanctioned transport.
- **Embed OPA's `rego` library in core** — rejected (ADR-0072/dependency-scout): ~50 transitive deps in the content-blind spine; the engine must be out-of-process/external, not linked into core.
- **REST to an OPA server first** — deferred: a fine transport (lower latency), but it needs a running OPA server; subprocess needs only the binary and is the simplest real integration to ship first.
