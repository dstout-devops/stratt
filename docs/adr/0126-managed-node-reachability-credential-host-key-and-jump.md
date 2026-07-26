# ADR 0126 — Reaching a managed node: the connection credential, the host key, and the jump path

- **Status:** **Proposed** (2026-07-25, steward). Charter review by hand (this session's rules bar the
  subagent); §1.1/§1.2/§1.4/§2.4/§2.5/§1.8/§9 answered inline. **No new dependency.**
- **Date:** 2026-07-25
- **Deciders:** steward
- **Charter sections:** §1.1 (type the seams), §1.2 (projections, never a second truth), §1.4 (boring
  spine — core authors no tool key), §2.4 (no two facts that can disagree), §2.5 (credentials brokered,
  never baked), §1.8 (never hide diagnosis), §9 (no ontology creep)
- **Supersedes nothing. Completes ADR-0084 D4, which was never built.**

## Context

`enterprise-readiness.md` **PLG-1** is narrowed to one half — **reachability to an operator-owned fleet** —
and names this plugin as its owner:

> Bastion/jump support and Site-local execution via the pull agent (ADR-0032) are unproven, and every
> managed node we converge is one we run.

Scoping that turned up something that has to be fixed **first**, because building on top of it would
introduce the very defect it is meant to remove.

### ADR-0084 D4 describes a mechanism that does not exist

ADR-0084 D4 says, of the machine credential:

> the shim points `ansible_ssh_private_key_file` at the mount … This mirrors AWX's inventory-host
> (`ansible_host`) vs. machine-credential (key/user) split exactly.

**There is no such code.** `plugins/ansible/` resolves a CredentialRef to its in-pod path in exactly one
place — `vaultPasswordFile` (`shim.go:111`), for `--vault-password-file`. The **connection** credential
was never wired. What ships instead is every Workflow hand-writing it:

```yaml
# estate/workflows/web-server-configure.yaml, demos/app-cert/.../app-install-with-cert.yaml,
# demos/app-cert/.../vacuous-run-guard.yaml — copy-pasted, all three
ansible_user: appops
ansible_ssh_private_key_file: /tmp/app-node-key
ansible_ssh_common_args: "-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null"
```

Three consequences, and they are the whole of this ADR:

1. **§2.4 — two facts that can disagree.** The Step's `credentialRefs` says which credential is
   authorized; `extraVars.ansible_ssh_private_key_file` says which file is used. Nothing makes them the
   same, and the second is a literal path in Git.
2. **§2.5-adjacent — the insecure shortcut is the copy-paste default.** `StrictHostKeyChecking=no` with
   `UserKnownHostsFile=/dev/null` disables host-key verification **and remembers nothing**, so every
   connection is a fresh trust-on-first-use with no protection at all. It appears in the reference estate,
   which is what an operator copies. This is precisely PLG-1's "dev convenience baked in as a design
   assumption."
3. **The inert-mechanism shape again.** A documented mechanism nobody built, invisible because the
   declarations worked around it.

**This blocks the jump work.** A jump host is configured through `ansible_ssh_common_args`, and that key
is currently hand-written YAML. Adding `ProxyJump` on top would mean two declarations of one Ansible key
with no stated winner — introducing a §2.4 defect while claiming to close a reachability gap. **The shim
must own the connection keys before it can render a jump.**

### What already ships that this must reconcile with

| Machinery                                                        | Where                          | Bearing                                            |
| ---------------------------------------------------------------- | ------------------------------ | -------------------------------------------------- |
| `mgmt.address` — a **closed** `{address, port?}` Facet           | ADR-0084 D1; §9                | A jump host must NOT become a third field here     |
| Core resolves the Facet → typed `Target.Address`; plugin renders | ADR-0084 D2/D3                 | The jump path follows the identical rule           |
| `vaultPasswordFile` — CredentialRef → in-pod path, diagnosed     | `shim.go:111`                  | The proven pattern D1 reuses rather than invents   |
| `CredentialRefs []string` — a **list**                           | `types/workflow.go:84`         | A second (bastion) credential needs no new channel |
| `Relation.Type` is a **free string**; `placed-in` etc.           | `types/entity.go:42`; ADR-0059 | A jump edge needs no schema change                 |
| Sites = remote execution loci; agent is a relay, not a governor  | ADR-0032/0049                  | The other answer to reachability — see D4          |
| Findings + Facet claim machinery                                 | ADR-0019/0041/0042             | Where a changed host key belongs                   |

## Decision

### D1 — The connection credential is resolved from its mount, by the shim, from a typed `connection` block

`ansible.input.v6` adds a `connection` object, modelled field-for-field on the shipped `vault` block:

```yaml
connection:
  user: appops
  credentialRef: app-node-key # already on the Step's credentialRefs — no second channel (§2.5)
  file: id_ed25519 # optional; required-and-diagnosed when the ref injects several
```

The shim resolves `credentialRef` → `/runner/credentials/<ref>/<file>` and renders
`ansible_ssh_private_key_file` **itself**; `user` renders `ansible_user`. Core still authors no Ansible
key (§1.4, ADR-0084 D3), and the authorized credential and the used file become **one fact** (§2.4).
A `credentialRef` not on the Step fails loudly, naming the fix — the `vaultPasswordFile` diagnosis,
reused verbatim.

**The shim stages the key at 0600, and this is the part the hand-written approach was really
working around.** `dispatch` projects credential files at **0440** — group-readable on purpose, so the
non-root execution pod can read them at all under its `fsGroup`. ssh then refuses such a key outright
(_"Permissions 0440 are too open"_), so **the mount cannot be handed to ssh directly**. Every SSH-using
play was already doing this copy by hand as a bootstrap task — `demos/app-cert` copies
`/runner/credentials/app-node-ssh/id_ed25519` to `/tmp/app-node-key` at 0600 before it can connect.

The shim doing it once is strictly better than each play remembering to: it is bounded to the runner
directory that is torn down with the Run (rather than `/tmp`), the mode is correct by construction rather
than per-author, and it stops being a step an author can forget. This was found by checking the mount
mode before deleting the demo's copy task — deleting it on the strength of the ADR text alone would have
shipped a D1 that broke every SSH Run.

**Hand-written connection keys in `extraVars` are refused, not merged.** `ansible_user`,
`ansible_ssh_private_key_file` and `ansible_ssh_common_args` in `extraVars` become a declaration-time
error naming `connection` as the place they belong. Merging them would recreate exactly the
two-sources-one-fact problem this decision removes, and silently losing to the shim would be worse.

### D2 — Host-key verification is a typed policy that defaults to verifying, and the honest limit is stated

`connection.hostKeyChecking: strict | accept-new | off`, **defaulting to `accept-new`**, never `off`.
Turning verification off becomes an explicit, reviewable word in Git instead of an argument buried in a
copy-pasted flag string.

**Why `accept-new` and not `strict` as the default.** A provisioning platform routinely converges a host
it built minutes ago, which by definition has no prior key. `strict` would fail every first contact, so
defaulting to it would guarantee operators set `off` — a default nobody can use is worse than an honest
one. `accept-new` accepts a first key and **refuses a changed one**, which is the property that matters.

**The honest limit, stated because it is the part that is easy to fake.** `accept-new` is only worth
anything if the accepted key is _remembered_; today `UserKnownHostsFile=/dev/null` guarantees it is not.
So D2 ships the policy **and** a per-Run known-hosts file, which makes a key change detectable within a
Run but not across Runs. **Cross-Run memory is deliberately not solved here**, and the right answer is
already visible in the model: a host key is an observed fact about an Entity, so it belongs in a Facet,
`known_hosts` is _rendered from the graph_, and a changed key is a **drift Finding** — projections and
Findings doing exactly what they exist for (§1.2). That is a decision with its own blast radius (a new
Facet, a claim rule, a Finding kind) and it gets its own ADR rather than being smuggled in here. Until
then this ADR claims only what it delivers: **explicit policy, secure default, in-Run detection.**

### D3 — A jump host is a **Relation** to an Entity, not a string in a Facet

`reached-via`, a free-string `Relation.Type` (no schema change, exactly as ADR-0059's `placed-in`),
directed host → bastion **Entity**. Core resolves the chain into a new typed `Target.Jump []JumpHop`
and the **shim** renders `-o ProxyJump=…` into `ansible_ssh_common_args`.

**Why a Relation rather than a field on `mgmt.address`.** ADR-0084 D1 made that schema closed on purpose,
"so a reachability seam never grows into a device ontology" (§9) — and the Relation is genuinely the
better model, not merely the permitted one:

- The bastion's address comes from **its own `mgmt.address`**, so there is no second copy to drift out of
  sync with the first (§2.4). A string field would duplicate a fact the graph already holds.
- **Multi-hop composes** as a chain of edges, with no schema growth.
- The bastion is usually a managed node in its own right — patched, audited, and reachable itself. Making
  it an Entity means it _is_ one, rather than being a string that happens to name one.

**Topology is graph data; authentication is Step config** — the same split ADR-0084 D4 drew between
address and machine credential. So how to authenticate _to the bastion_ is `connection.jump: {user,
credentialRef, file}`, and a second CredentialRef needs no new mechanism: `credentialRefs` is already a
list, and the existing per-name use-check already gates it (§2.5).

**Three failures are loud, and all three for the same reason: reaching a target _around_ its declared
bastion is worse than failing to reach it at all** (§1.8, matching ADR-0084 D2's no-silent-localhost
rule).

- A **cycle** — caught explicitly, so the diagnosis says "cycles" rather than "too many hops".
- A hop whose Entity carries **no `mgmt.address`** — a bastion nothing can reach is not a route. Refused
  at _both_ layers: core at resolve time, and the shim again before it renders, because a spec that
  simply omitted the hop would connect direct and look like it worked.
- **Two `reached-via` edges from one host** — an ambiguity core will not resolve. Picking one would be
  precisely the implicit precedence §2.4 forbids, and there is deliberately no tiebreak field to add.

**One Run renders one `ProxyJump`.** The connection vars are inventory group vars, so a slice whose
targets sit behind _different_ bastions cannot be rendered correctly, and it is **refused naming both
offenders** rather than rendered from one of the chains. Grouping targets by chain is the natural
extension of the Site grouping that already ships (ADR-0032) and is what that failure asks the author
for; the shim guessing would route the other targets through the wrong bastion.

**Per-hop key _binding_ is not solved.** A hop's key is offered via `-o IdentityFile`, which ssh treats
as additive across the chain rather than bound to a specific hop. Binding needs a generated `ssh_config`,
which is a larger seam than any shipping estate has asked for — stated here rather than implied by the
per-hop shape of `connection.jump`.

### D4 — Sites and ProxyJump are complementary; neither is "the" answer

Stated because picking one would be wrong in a way that is expensive to undo.

- **A Site is the better answer when you can place an agent** (ADR-0032/0049): execution happens _inside_
  the segment, so there is nothing to proxy, and the existing `mgmt.site` → `groupBySite` → per-Site
  dispatch already carries it end to end.
- **ProxyJump is for when you cannot** — a hardened bastion appliance you may not install software on, or
  a customer who will not run an agent. That is a common enough production reality that "deploy an agent"
  is not an acceptable only-answer.

They compose: a Site-dispatched Step may itself jump. Nothing here changes Site routing.

## Charter alignment

- **§1.1 / §9.** The connection seam is typed at the plugin boundary; the closed `mgmt.address` schema
  stays closed, and the jump path reuses Relations rather than growing a device ontology.
- **§1.2.** The bastion is an Entity; the topology edge is graph data written by the two authorized paths,
  never a hand-authored row.
- **§1.4.** Core resolves coordinates and authors **no** Ansible key; the shim renders every one.
- **§2.4.** The authorized credential and the used key file become one fact. Hand-written connection keys
  are refused rather than merged. The bastion's address has exactly one home.
- **§2.5.** No new credential channel: `credentialRefs` is already a list, and the existing per-name
  use-check gates the bastion credential exactly as it gates the target's.
- **§1.8.** Every new refusal names its offender — the unmounted ref, the ambiguous file, the extraVar
  that belongs in `connection`, the hop with no address, the cycle.

## Consequences

- **Positive.** ADR-0084 D4 becomes true. The reference estate stops teaching
  `StrictHostKeyChecking=no` by example. Bastion'd fleets become reachable, which is PLG-1's remaining
  half. A `reached-via` chain composes to multi-hop for free.
- **Negative / trade-offs.** `ansible.input.v6` is a contract version bump, and the three estate/demo
  Workflows must move their connection keys into `connection:` — a deliberate breaking change, since
  leaving the old path working is what would keep the §2.4 defect alive. Host-key memory is **in-Run
  only**; the cross-Run answer is named, designed, and explicitly not delivered.
- **Follow-up booked, exactly one:** the host key as a Facet + drift Finding (D2's cross-Run half). It is
  booked rather than absorbed because it adds a Facet, a claim rule and a Finding kind — its own blast
  radius.

## Alternatives considered

- **Add `jumpHost` to `mgmt.address`.** Rejected in D3: ADR-0084 D1 closed that schema against exactly
  this, and a string would duplicate an address the bastion's own Entity already carries.
- **Let the shim merge `extraVars` connection keys with the `connection` block.** Rejected in D1: a merge
  needs a winner, and a silent winner between an authorized credential and a hand-written path is the
  §2.4 defect restated.
- **Default `hostKeyChecking: strict`.** Rejected in D2: it fails every first contact against a
  freshly-built host, so it would be routinely disabled — a secure-looking default that trains operators
  to turn security off.
- **Solve reachability with Sites alone.** Rejected in D4: correct when an agent can be placed, and not
  an answer at all when it cannot.
