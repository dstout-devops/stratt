# ADR 0153 — A connection type, and a password that is only ever a path

- **Status:** **ACCEPTED** (promoted 2026-08-01 by the steward; proposed 2026-07-31). The credential
  forms are implemented and guarded — `TestInventoryCarriesCredentialPathsAndNeverMaterial` now checks
  D3's path-not-value rule by VALUE rather than by variable name, after the older name-based guard was
  found to pass anything not called `*pass*`. **Unchanged caveat, restated rather than quietly dropped:**
  `network_cli`/`netconf` remain shipped-but-not-live-proven — no FRR/cEOS target exists in CI, and D7's
  lesson is that a declared connection type is not a proven one. Charter review by hand — this session's rules bar the
  subagent; §1.1/§1.4/§1.8/§2.4/§2.5/§9 answered inline. **No new dependency.**
- **Date:** 2026-07-31
- **Deciders:** steward
- **Charter sections:** §1.1 (type the seams), §1.4 (boring spine — core authors no tool key),
  §1.8 (never hide diagnosis), §2.4 (no two facts that can disagree), §2.5 (credentials brokered,
  never baked), §9 (no ontology creep)
- **Supersedes nothing. Extends ADR-0126** (the typed `connection` block) **and ADR-0117** (the typed
  run knobs). Closes ANS-001's network half, ANS-010, ANS-011.
- **AMENDED same day with D7, forced by verification.** D1 shipped before anyone checked whether the
  running image could load the plugins the enum admits. It cannot: `network_cli`/`netconf` are not in
  ansible-core, so D1's own argument applied to the Contract and not to the runtime. See D7.

## Context

[`ansible-tool.md`](../parity/ansible-tool.md) §3 is blunt about the largest gap in the platform:

> `connectionVars` emits exactly three things: `ansible_user`, `ansible_ssh_private_key_file`, and
> `ansible_ssh_common_args`. … So the Actuator can reach **Linux/Unix over SSH with a private key**,
> and the control node itself. … This is not a depth gap, it is a **reach gap**: for those estates the
> answer today is "you cannot use Stratt," and no current document says so.

And the tell that it was never a decision: the `mgmt.address` Facet schema describes itself as feeding
"the ansible/ssh/**winrm** Actuator" — the coordinate was designed for a connection type the execution
path never grew.

Three findings sit in this one place, and the audit already observed they are one shape:

| ID          | Gap                                                                |
| ----------- | ------------------------------------------------------------------ |
| **ANS-001** | No connection type but ssh+local; no password auth at all          |
| **ANS-010** | No become password — a sudo-with-password target cannot escalate   |
| **ANS-011** | One vault identity only; a repo using two `--vault-id`s cannot run |

> ANS-010 · Become password. Small, and blocks a common sudo posture. **Naturally batched with
> ANS-001, since both are "a credential form the connection surface has no shape for."**

That framing is right, and it is what this ADR decides: **the connection TYPE is the easy half; the
credential FORM is the argument.**

### Prior art this must reconcile with (scanned by hand — subagents barred this session)

- **ADR-0084** drew the split this lives inside: the ADDRESS is a `mgmt.address` Facet the core
  resolves into a typed `Target` coordinate; the CREDENTIAL is the Step's. Nothing here may put an
  address in params.
- **ADR-0126** built the typed `connection` block (v6): `user`, `credentialRef`, `file`,
  `hostKeyChecking`, `jump`. This ADR **extends that block** rather than opening a second one — a
  second connection channel is exactly the §2.4 hazard D1 closed when it moved connection keys out of
  `extraVars` and started **refusing** them there.
- **ADR-0117 D1** made the run knobs typed values, not a flag string, "so a future content-blind
  Control can gate it". `become` is one of those knobs and grows a field here on the same terms.
- **ADR-0117 D6 / ADR-0134** — core never parses play content, and never learns an ansible key. Every
  `ansible_*` var and every flag in this ADR is rendered **by the shim**, on the far side of the port.
- **ADR-0132 D4** — a new Actuator input Contract version is a **sibling file**, additive: the
  registry keeps ONE live `actuators/ansible.input` (the highest version) and **a Step cannot pin a
  version**. So v8 must keep every v7 field valid, or it breaks estates on upgrade.
- **ADR-0009 / 0052 / 0092, §2.5** — a credential is a brokered `CredentialRef`; the per-name
  use-grant check at dispatch is the single authorization path, and material never lands in the graph
  or in artifacts.
- **ADR-0051 MF4** — the rendered inventory is the View's resolved target set. Nothing here widens it.

## Decision

### D1 — `connection.type` is a closed enum, and it contains only what we can verify

`ansible.input.v8` adds `connection.type`: **`ssh`** (default) · **`network_cli`** · **`netconf`**.

**`winrm` and `psrp` are NOT in the enum.** Windows is the single most-asked-for row in the register,
and the honest position is that we cannot verify it: there is no freely-runnable Windows target in
CI, so a `winrm` value would ship as a code path nothing ever executed. An enum that **accepts** a
value the shim has never honored is strictly worse than one that rejects it — rejection fails at
estate load with a message naming the gap, acceptance fails at 3 a.m. on a fleet someone migrated.

This is the same rule ADR-0151 D3 applied to unimplemented substrates and the same rule the
`/api/v2` schedules family applied to an unconvertible cron: **no answer beats a plausible wrong
one.** When a Windows target can be stood up and driven end to end, `winrm` is one enum value and a
credential form that already exists after this ADR.

**`httpapi` is also out, for a different and smaller reason:** it needs its own transport knobs
(`use_ssl`, `validate_certs`, port) and there is no defensible default for `validate_certs` — `false`
is a security posture nobody chose and `true` breaks every appliance with a self-signed certificate.
That is its own decision, not a line in this enum.

**`local` is deliberately NOT a value here.** It already arrives as a property of the TARGET —
`mgmt.address: local` renders `ansible_connection=local` per host (`ansible.go:88`). Putting it in
params too would be two homes for one fact (§2.4), and the host var would silently win over the group
var. See D6 for the refusal that makes that structural rather than conventional.

### D2 — the netcommon family needs `networkOS`, it is REQUIRED, and it is per-RUN

`connection.networkOS` renders `ansible_network_os` (e.g. `cisco.ios.ios`, `arista.eos.eos`,
`frr.frr.frr`). It is **required when `type` is `network_cli` or `netconf`** and **refused otherwise**.

Required rather than defaulted because ansible cannot connect without it and there is nothing to
infer it from. A default would pick a vendor's command set for a device of another vendor: the
connection succeeds, the first task issues the wrong syntax, and the diagnosis points at the play.

**It is a per-Run value, and the honest limitation is that a mixed-vendor View cannot run as one
Step.** The connection vars are inventory GROUP vars — one `[all:vars]` per Run — which is the same
constraint `jumpChainOf` already lives under, and it takes the same resolution: **refuse the
ambiguity rather than let the shim pick.** Splitting a mixed fleet into per-vendor Views is what the
estate should express anyway.

The alternative — a per-host `network.os` Facet — was considered and **declined for now**: nothing
demands it yet (§1.1: every Facet schema must be demanded by a shipping Contract), and `mgmt.address`
is explicitly closed against growing into a device ontology (§9). Booked, not built.

### D3 — a password is a PATH. It is never a value, anywhere.

**This is the decision the audit said needed the argument, and the mechanism turns out to be better
than the one the gap register sketched.**

`ansible-core` (the EE pins `>=2.19`) takes all three secrets as **file paths**:

```
--connection-password-file  CONNECTION_PASSWORD_FILE   # the SSH / network-device password
--become-password-file      BECOME_PASSWORD_FILE       # the sudo password
--vault-password-file       VAULT_PASSWORD_FILES       # repeatable
--vault-id                  VAULT_IDS                  # repeatable, id@path
```

Verified against the pinned interpreter, not assumed from documentation.

So v8 adds `connection.passwordRef` and `become.passwordRef`, each `{credentialRef, file}` — the
**same shape** `vault` and `connection.credentialRef` already use, resolved by the **same**
`credentialFile` helper, gated by the **same** per-name use-grant check at dispatch. No new credential
channel, no new authorization path (§2.5, ADR-0009/0052).

**The rejected alternative is the one everybody writes first:** render `ansible_password` as an
inventory group var. It is wrong, and not marginally:

- `renderInventory` writes group vars into `inventory/hosts`, and `writeInventory` writes that file at
  **0644** inside the private data dir — **beside `artifacts/`**, which ansible-runner populates and
  the Run can carry. §2.5 says secret material is "never logged, never written to the graph or
  artifacts". A password in `[all:vars]` is exactly that.
- The same objection kills `extraVars`, which is written to `env/extravars` in the same directory —
  and which v6 already refuses connection keys in, for a related reason.
- A Jinja indirection (`ansible_password={{lookup('file','/runner/credentials/…')}}`) would keep the
  material out of the file, and was the fallback if no flag existed. It does not need to be built:
  the flag surface is simpler, is already how `--vault-password-file` works here, and renders as its
  own argv token — the property ADR-0117 D1 typed these fields to get.

**The password file is used at its mount and never staged.** Contrast `connection.credentialRef`,
whose 0440 mount ssh _refuses_ ("Permissions 0440 are too open") and which therefore has to be copied
to 0600 — a copy this ADR does NOT extend to passwords, because ansible applies no permission check
to a password file and a second copy of a secret is a second place it can leak from.

### D4 — `vault` accepts one identity or many, in ONE field

v7's `vault` is an object. v8 lets it be **either that object or an array of them**, each entry
gaining an optional `id`:

- an entry with **no `id`** → `--vault-password-file <path>` — byte-identical to today's rendering;
- an entry **with `id`** → `--vault-id <id>@<path>`.

**One field with two shapes rather than a second field**, deliberately. A `vaultIds` alongside `vault`
would be two declarations of one concept, and the moment both are set there has to be a rule about
which wins — the implicit precedence §2.4 refuses outright. Same field, normalized to a list by the
shim, no winner to pick.

And it must be non-breaking, not merely polite: the registry keeps one live `actuators/ansible.input`
and **a Step cannot pin a version** (ADR-0132 D4), so an array-only v8 would fail every shipped
object-form Step the moment v8 landed — loudly, but still broken.

Duplicate `id`s are refused: two files claiming one vault identity is an ambiguity ansible resolves by
order, which is a silent winner by another name.

### D5 — become grows a password, and nothing else

`become.passwordRef` renders `--become-password-file`. `become` stays the typed
`{enabled, user, method, passwordRef}` shape ADR-0117 D1 established; the escalation method enum is
untouched.

A `passwordRef` **without** `enabled: true` is refused. Supplying a sudo password for an escalation
that is not requested means one of the two is a mistake, and guessing which produces either a
pointless credential mount or a run that quietly does not escalate.

### D6 — a type that contradicts the target is refused, not resolved

`connection.type` renders `ansible_connection` as a **group** var. A target whose `mgmt.address` is
the reserved `local` renders `ansible_connection=local` as a **host** var — and in ansible, host vars
win.

That is implicit precedence sitting inside two correct-looking declarations, so the shim **refuses
the combination**: a non-`ssh` `connection.type` with any `local` target fails the Run with a message
naming both the type and the offending target. Refusing is the only option that cannot silently
connect the wrong way (§1.8, §2.4).

### D7 — a type the RUNNING IMAGE cannot honor is refused too (correction, found by verifying)

**D1 was written and shipped before this was checked, and checking it found the ADR half-applied.**
`network_cli` and `netconf` are not in ansible-core. Measured against the interpreter the EE pins:

```
$ ansible-doc -t connection network_cli
[WARNING]: Error loading plugin 'ansible.netcommon.network_cli':
           No module named 'ansible_collections.ansible.netcommon'
[WARNING]: network_cli was not found
```

So a Contract that ACCEPTS the value on an image that cannot load the plugin produces a declaration
which passes review, passes the estate load, and passes every unit test in this ADR — then dies at
connect time naming a python module the estate never wrote. That is the identical failure
`ee/content/platform.requirements.yml` was written about (`community.general.apk`: "naming a
collection the play never mentions"), and it is D1's own argument coming back through the door D1
did not close: **an enum must not admit a value the RUNTIME has never honored either.**

**The shim refuses it, reading the image's own answer.** Every Stratt EE publishes
`/etc/stratt/ee-content.json` — ee/content.py's "run-visible manifest (§1.8) … what the image
ACTUALLY contains". `requireConnectionCollection` reads it and fails with the missing collection,
what IS installed, and where the fix belongs.

**Not a probe, and that distinction was also measured.** The obvious check is `ansible-doc -t
connection <type>` and an exit code — **it exits 0 for a plugin that does not exist**, so the obvious
check silently passes. The manifest is deterministic, needs no subprocess, and is the same artifact
an operator descending into the Run already sees.

**Order matters and is fixed:** every declaration-level refusal (D1/D2/D6) is reported BEFORE the
image check, so an operator is never sent to rebuild an EE over a typo they could fix in YAML. An
UNREADABLE manifest is its own third outcome — neither present nor missing — because guessing either
way turns an image problem into a connection problem.

**The image half: `ee/content/network.requirements.yml`**, a variant, not a floor entry. The floor's
own rule is that it carries "exactly what the PLATFORM'S OWN shipped content requires", checkable
against `plugins/*/estate/content/`; no shipped content root speaks to a device, so widening the
floor would dissolve the rule that keeps it checkable. ADR-0117 D3 already named the home for
"content my estate needs".

It ships **`ansible.netcommon` only** — the connection plugins, which is the mechanism the enum
promises. A vendor collection is deliberately absent: `networkOS` names a platform whose
cliconf/terminal plugins live in that vendor's collection, and which vendors a fleet needs is exactly
the adopter-shaped question a variant exists for. Shipping one would be picking a fleet for them.

**Verified against real images, both directions:**

| Image                            | `ansible-doc -t connection network_cli` | manifest                              |
| -------------------------------- | ---------------------------------------- | ------------------------------------- |
| `stratt-ee:dev` (platform floor) | ❌ not found                              | `community.general` only → shim refuses |
| `stratt-ee-network:dev`          | ✅ resolves (both `network_cli`, `netconf`) | lists `ansible.netcommon` → shim allows |

**This closes the collection half of the live gap, and not the device half.** A collection that
installs is not a connection that works: no real device has been driven yet, and that still needs an
FRR or cEOS target in CI.

## Consequences

**What an adopter can now answer differently.** "Do you run my network fleet?" moves from **no** to
**yes over `network_cli`/`netconf`, with the device credential brokered like every other**. "My hosts
only take a password" moves from **no** to **yes**. "My sudo needs a password" moves from **no** to
**yes**. "My repo uses two vault ids" moves from **no** to **yes**.

**What it does not change.** Windows is still **no**, and now says so at estate load rather than at
run time. `httpapi` is still no. Those two rows stay red in the register, with the blocker named.

**No live proof yet, and this is the honest limit.** The rendering is unit-tested — every flag, every
refusal, every ordering — but no network device has been driven end to end from this repo. The gap is
a CI-runnable target: an FRR or cEOS container plus the matching collection in the EE image. Booked as
the live half, in the shape PLG-1's bastion half is booked. **A unit-green connection type is not a
proven one**, and this ADR does not claim otherwise.

**Contract surface.** `ansible.input.v8` is a sibling of v7 (ADR-0132 D4), additive: every v7 field is
retained with its meaning, and `vault`'s object form still validates. Version 8 becomes the one live
`actuators/ansible.input`.

**Blast radius.** All of it is inside the plugin and its Contract. Core learns no new key, no new
Facet ships, and the port is untouched — which is the property ADR-0084's address/credential split was
drawn to give this exact change.
