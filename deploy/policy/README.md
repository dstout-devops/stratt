# External policy engine (OPA / Kyverno) over the subprocess transport

Stratt's Policy Decision Point is a swappable port (ADR-0072). This directory is
a **reference** for running an external engine — here **OPA** — as the PDP over
the **subprocess transport** (ADR-0074, charter §1.5). The engine is external,
swappable, and bypassable; nothing here is core.

## Wire it up

Point strattd at a command that speaks the Decision contract:

```sh
STRATT_POLICY_ENGINE=opa
STRATT_POLICY_EXEC_CMD=/etc/stratt/policy/opa-decider.sh
# (STRATT_POLICY_BYPASS=true disables governance entirely — recorded, never silent)
```

strattd then invokes `opa-decider.sh <op>` for each decision, where `<op>` is
`decide` (the gate PEP) or `admit` (the admission PEP). Unset ⇒ the built-in CEL
provider (the zero-dependency default).

## The contract (the sovereign Decision, over stdin/stdout)

- **stdin** — the request JSON.
  - `decide`: `{"controls": [...], "context": {actor, environment, blastRadius, changeClass, riskScore, labels, …}}`
  - `admit`: `{"object": {kind, spec, labels, …}, "controls": [...]}`
- **stdout** — a `Decision` JSON: `{"outcome": "allow|deny|require_approval|escalate", "reasons": [{code, message}], "obligations": [{type, params}]}`.
- **Fail-closed:** a non-zero exit, unparseable output, or an unrecognised
  `outcome` is treated by Stratt as a **deny** — never a silent allow (§1.8).

The engine (the Rego in `policy.rego`) is the operator's governance content,
living entirely outside the Stratt spine (§1.1/§1.4). Swap OPA for Kyverno-JSON
(or any tool) by pointing `STRATT_POLICY_EXEC_CMD` at a wrapper that emits the
same Decision JSON.

Files:
- `opa-decider.sh` — the wrapper: request on stdin → `opa eval` → Decision on stdout.
- `policy.rego` — an example policy emitting Decisions for `decide` and `admit`.
