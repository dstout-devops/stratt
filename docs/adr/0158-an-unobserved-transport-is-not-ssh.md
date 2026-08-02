# ADR 0158 — An unobserved transport is not ssh

- **Status:** **Proposed** (2026-08-02, steward). Charter review by hand — this session's rules bar
  the subagent; §1.2/§1.8/§2.4/§9 answered inline. **No new runtime dependency.**
- **Date:** 2026-08-02
- **Deciders:** steward
- **Charter sections:** §1.2 (projections, never a second truth — observed, not inferred), §1.8
  (never hide diagnosis), §2.4 (no implicit precedence — the anti-GPO axiom), §9 (no ontology creep)
- **Extends ADR-0156** (the transport is observed) with the case it did not answer: what the SHIM
  should do when nothing observed one. **Scopes ADR-0153 D1**, whose "empty type means ssh" is the
  behaviour this ADR narrows.

## Context

ADR-0156 made the reach method an observed Facet: `mgmt.address` says WHERE, `mgmt.transport` says
BY WHAT MEANS, both projections of what a provider actually did. The shim renders per-host vars from
it, so one Assignment converges a pod, a VM and an EC2 instance in one Run.

It did not decide what a target with **no** `mgmt.transport` means. The shim inherits ADR-0153 D1's
rule — an empty `connection.type` renders ssh — so absence silently becomes **ssh**.

**Absence is overloaded across shipped providers, and both cases are real:**

| Provider      | `mgmt.address`                       | `mgmt.transport`                             | What absence means |
| ------------- | ------------------------------------ | -------------------------------------------- | ------------------ |
| `kubecompute` | on `PodRunning`                      | on `PodRunning` — **same gate, same upsert** | not built yet      |
| `vcenter`     | `Guest.HostName` / `Guest.IpAddress` | `ToolsRunningStatus == guestToolsRunning`    | **tools stopped**  |
| `awsec2`      | the instance's observed name/address | **never written, deliberately**              | **ssh, correctly** |

`awsec2` withholds it on purpose and the reasoning is ADR-0142 D4's: `KeyName` means a key is
_authorized_, not that sshd is _listening_, and SSM needs a different API. Writing `ssh` there would
be **computing a reach fact**, which that ADR forbids outright. So the provider that most needs ssh
is precisely the one that cannot say so.

`vcenter` gates the two facets differently, and vCenter **caches guest info**. A VM whose tools stop
keeps a stale `mgmt.address` and loses its `mgmt.transport` — after which the shim reaches for ssh on
a host whose last observed answer was `vmware_tools`.

### What this ADR is NOT, recorded because the first version of it was wrong

This was booked as a **race**: "the build's terminal projection supplies the address, the Syncer
supplies the transport, so a host is addressable before its reach method is known," with timestamps
two minutes apart cited as evidence.

**That mechanism does not exist.** `kubecompute`'s build calls the same `project()` the Syncer does,
gating both facets on `PodRunning`, and emits neither at build time — its own comment says so. The
timestamps cited were the latest write of a Facet the Syncer rewrites every cycle; a recency stamp
was read as a first-appearance stamp. The converge that motivated the entry failed for reasons since
fixed (no brokered kubeconfig, then a stale EE image).

Correcting it is what produced the real question, which is smaller and harder: **not a timing window,
but a value that means two different things depending on which provider declined to write it.**

## Decision

### D1 — the shim must not infer ssh from silence

An absent `mgmt.transport` means **the reach method is unknown**, never "ssh". Rendering ssh for a
target nobody observed is the shim asserting a fact about the host — the §1.2 line ADR-0142 D4 drew
for reach coordinates, applied to the reach _method_. It is also §2.4's anti-GPO axiom in miniature:
absence resolving to a default is precedence with nobody's name on it.

### D2 — ssh becomes a DECLARED type for hosts nothing observes, not a default

ADR-0156 D5 already scoped `connection.type` correctly: declared is right for **a device an operator
names**, observed is right for **a host a provider built**. An EC2 instance whose transport cannot be
observed falls on the declared side, and the Step says `connection.type: ssh` — which ADR-0153 D1
already accepts and which the shipped estates already carry for their hand-declared nodes.

This costs a migration: every Step converging hosts with no observed transport must name `ssh`. That
is the cost of making the estate state what it means, and it is paid once, visibly, in Git — rather
than by every future Run guessing.

### D3 — a target with neither an observed transport nor a declared type is REFUSED

The shim fails the Run before it starts, naming the target and both remedies. Today such a target
produces ansible's `unreachable`, which names the guest for a decision the control node made — the
§1.8 failure this arc has now paid for four separate times.

**Refuse, do not warn.** A warning on a converge that then attempts ssh is strictly worse than
either alternative: it runs the wrong thing and reports it quietly.

### D4 — `mgmt.transport` gains no `unknown` value

The tempting fix is a third state written by providers that cannot observe. It is refused for the
same reason `awsec2` writes nothing today: a provider asserting "unknown" is asserting something, and
the Facet's whole contract (ADR-0156 D4) is that it carries what was OBSERVED. Absence already
expresses "nothing observed" precisely. The defect was never the encoding — it was the shim reading
absence as an answer.

## Consequences

- **Every estate converging EC2 or hand-declared hosts must declare `connection.type: ssh`.** A
  migration, and the compile-time failure names the Step, so it cannot be discovered at run time.
- **`demo:ec2-only` is unaffected** — it converges nothing. `demos/app-cert` and the reference
  estate's declared nodes need the line.
- **The vcenter stale-address case becomes diagnosable**: tools stop, transport disappears, and the
  next converge refuses by name instead of silently trying ssh against a VM reached through vCenter.
- **A cost worth stating:** a mixed View holding both observed and unobserved hosts now needs the
  declared type for the unobserved half, and the declared type is a GROUP var (ADR-0156's own
  argument). Rendering it per-host is required, and is a shim change rather than an estate one.

## Verification

Not shippable on assertion. This ADR is **Proposed** and its implementation owes:

- a shim unit test that a target with no transport and no declared type is REFUSED, falsified by
  removing the check;
- a live demo run proving the refusal message names the target and both remedies;
- `demo:app-cert` green with `connection.type: ssh` declared, proving the migration is what the
  consequence section claims and not larger.

**No live proof is claimed here.** The last four ADRs on this arc each cost a real defect by shipping
a seam that was reviewed and never executed — including ADR-0156 itself, which shipped a transport
that could not authenticate. This one is a decision, not a landing.
