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
  migration, and the refusal names the Step's target rather than surfacing as `unreachable`.
- **`demo:ec2-only` is unaffected** — it converges nothing. `demos/app-cert` and the reference
  estate's declared nodes need the line.
- **The vcenter stale-address case becomes diagnosable**: tools stop, transport disappears, and the
  next converge refuses by name instead of silently trying ssh against a VM reached through vCenter.

### Corrected while implementing — three claims above were wrong

Recorded rather than quietly fixed, because two of them were the reason this ADR looked expensive.

1. **The per-host rendering cost does not exist.** The draft said a mixed View needs the declared
   type for its unobserved half, that the declared type is a GROUP var, and that rendering it
   per-host is therefore required — a shim change. It is not, and the reason is structural rather
   than lucky: `ssh` is the ONLY declared type that can coexist with an observed transport, because
   ADR-0156 D5 refuses every other one outright — and `connectionTypeVars` renders **nothing at all**
   for ssh, since ansible's own default already is ssh. There is no `ansible_connection` group var
   for a host var to fight with. The observed half keeps its per-host vars, the unobserved half falls
   through to the default, and the two never meet. Pinned by
   `TestMixedViewNeedsNoPerHostRenderingOfTheDeclaredType`, which fails if ssh ever starts authoring
   a group var. **This ADR costs no shim rendering change.**
2. **It is not a compile-time failure.** The draft claimed the migration "cannot be discovered at run
   time" because the failure is at compile time. It cannot be: a Step's targets come from a View
   resolved at LAUNCH, and `connection.type` is an ansible params field only the shim reads. D3 says
   the right thing — the shim refuses before the play runs — and the consequence contradicted it.
   What is true is the weaker, still-useful claim: the refusal happens before `ansible-runner` is
   spawned, so nothing is reached and nothing is changed.
3. **The migration is ~3× the size stated.** Not two demo Steps and "the reference estate's declared
   nodes" but **9 Steps across 8 estate files** — and five of those Steps (`access-apply`,
   `access-revoke`, `fileset-apply`, `fileset-revert`, `linux-onboard`) carried **no `connection`
   block at all**, so they were invisible to a grep for `connection:` and are the shape this ADR
   most affects. Plus **17 test functions** in `plugins/ansible`, every one of which relied on
   empty-means-ssh. `demos/region-to-cert` needed nothing: its targets are kubecompute pods that
   observe `kubectl`.

### A drift this surfaced, not caused

`contracts/facets/mgmt.transport.schema.json` describes itself as "awsec2 observes `aws_ssm` or
`ssh`". **Neither is written by anything.** `plugins/awsec2/normalize.go` deliberately writes no
transport at all (its own comment says why), and `aws_ssm` has no writer pending an SSM client. So
**no shipped provider emits an observed `ssh` transport** — which is what makes `ssh` a DECLARED
value in practice (D2) and is why the test fixtures migrated here declare it rather than observing
it. The schema description should be corrected to describe what is written; booked, not done here.

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

### Landed so far (2026-08-02) — the first item, and NOT the other two

`requireReachMethod` in `plugins/ansible/transport.go`, called from `shim.go` as the fourth reach
axis after the coordinate, image and credential checks. Refuses when nothing observed a transport and
nothing declared a type; exempts a `local` target, which states its reach method through
`mgmt.address`'s reserved value (ADR-0153 D6).

**Falsified two ways, because the unit tests alone do not cover the wiring:**

- Disable the collection arm → 5 tests fail, including the end-to-end one.
- Delete the call in `shim.go` → **exactly one** test fails,
  `TestShim_UnobservedTargetRefusesBeforeAnsibleIsSpawned`, which drives `Run` and asserts
  `ansible-runner` was never spawned. Without it a shipped-but-uncalled check would have passed the
  whole suite — the defect class this repo keeps rediscovering.

`task ci` EXIT=0 with the estate migration applied.

**Still owed, and the ADR stays Proposed until they land:** both live items. A refusal proven in a
unit test is a refusal proven against a fixture, and this arc's whole lesson is that the fixture is
not the estate.
