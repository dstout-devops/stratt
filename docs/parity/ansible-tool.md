# Ansible parity — content-root shape and execution surface

**Subject:** Ansible itself, not AWX. Two surfaces: what the `ansible-automation` **content** half sees
when it reads an Ansible project, and what the `ansible` **Actuator** can actually make
`ansible-playbook` do.

**Scope boundary.** Charter §9 forbids reinterpreting Ansible's execution model into a Stratt dialect —
"parsing to observe is fine; normalizing Ansible's execution model is the line not crossed." So the target
here is **not** "project everything in the repo." A row marked ⚪ below is usually that line, deliberately
held. The rows worth acting on are the ones where we fail to observe **structure** (which is graph-shaped)
rather than declining to reinterpret **behaviour** (which is not).

**Evidence base (2026-07-26).** Read from
[plugins/ansible-automation/content/](../../plugins/ansible-automation/content/),
[plugins/ansible/](../../plugins/ansible/), and
[contracts/actuators/ansible.input.v6.schema.json](../../contracts/actuators/ansible.input.v6.schema.json).
Ansible's own feature surface is the documented `ansible-playbook` CLI and the standard content-root
layout — a documentation reading, not a live comparison.

**Reachability is not re-audited here.** It is owned by **PLG-1** in
[enterprise-readiness](../enterprise-readiness.md), which is red and says exactly why (bastion/jump built
and unit-proven but with no live proof; Site-local dispatch wired but never executed live). Nothing below
restates it.

---

## Scorecard

| Area                     | Verdict                   | One-line                                                                                                                  |
| ------------------------ | ------------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| **Playbook discovery**   | 🟢 sound                  | Play-shape detection, not path convention — a role task file and a `requirements.yml` both correctly fail it              |
| **Content-root breadth** | 🟡 four artifact kinds    | Roles, collections, playbooks, inventory files. A shipped content root now **uses** a role (`content/webapp/roles/stratt_web_service`, included by all three web plays) and nested `vars/<app>/<os_family>.yml` — verified through the real ConfigMap path, not just a bind mount. Role **dependencies** (`meta/main.yml`) and the roles half of `requirements.yml` are still unread |
| **Variable layout**      | 🔴 invisible              | `group_vars/`, `host_vars/`, vars files — none observed; the estate cannot see where a value comes from                   |
| **Custom content**       | 🔴 invisible              | `library/`, `module_utils/`, filter/callback plugins — a repo's own modules are unprojected                               |
| **Run knobs**            | 🟢 typed and bounded      | 12 typed fields in `ansible.input.v6`, each rendered as its own token — an argument surface, not an injection one         |
| **Connection types**     | 🔴 **SSH-only, key-only** | No winrm, no network_cli; no SSH password, no become password. **Windows and network devices cannot be automated at all** |
| **Vault**                | 🟡 single identity        | One vault CredentialRef; `--vault-id` multi-identity unsupported                                                          |

**Bottom line.** The execution surface is genuinely good where it reaches — typed, bounded, reviewable,
and deeper than the AWX mirror of the same knobs. The one finding that changes what Stratt can be sold as
was **ANS-001**: the Actuator spoke SSH with a private key and nothing else, so a Windows estate or a
network fleet was not partially supported, it was unsupported. **ADR-0153 (2026-07-31) closed most of it**
— `network_cli`/`netconf` reach, plus the three credential forms (login/device password, escalation
password, multiple vault identities) — and the residue is now **Windows only**, stated in the enum rather
than discovered at run time. That residue is real and is not being talked around: see §3.

---

## 1. Content-root shape

What an Ansible project contains, against what the content half projects
([content/types.go](../../plugins/ansible-automation/content/types.go),
[content/normalize.go](../../plugins/ansible-automation/content/normalize.go)).

| Content-root element                                                | Observed?                           | Note                                                                                                                                             | ID          |
| ------------------------------------------------------------------- | ----------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------ | ----------- |
| Playbooks (any `.yml` that is a play sequence)                      | 🟢 `name`, `path`, `plays`, `hosts` | Detected by **shape**, not location — a top-level sequence of mappings with `hosts:` or `import_playbook:`. Pinned schema                        |             |
| `roles/*/`                                                          | 🟢 name, path, required, version, deps, galaxy_info | Widened by the content-depth batch (2026-07-31). Tasks/handlers/defaults stay unread — the §9 line                                |             |
| `roles/*/meta/main.yml` → `dependencies`                            | 🟢                                  | Projected as `depends-on` **relations**; all three Galaxy forms (bare string, `{role:}`, `{name:}`). Resolves to the in-tree role when there is one, else into the requirements space | ~~**ANS-004**~~ |
| `roles/*/meta/main.yml` → `galaxy_info`                             | 🟢 author, license, min version, platform names | Platform *versions* are not projected — a supported-version matrix is not estate structure                                          | ~~**ANS-004**~~ |
| `collections/requirements.yml` → `collections:`                     | 🟢 `name`, `version`, `source`      | Both Galaxy-legal forms (bare FQCN string and mapping) handled                                                                                   |             |
| `requirements.yml` → `roles:`                                       | 🟢 `name`, `version`, `src`         | Same `ansible.role` Kind as an in-tree role, marked `required` — one question, one Kind. Parsed PER ENTRY, because real files mix the bare-string and mapping forms in one list | ~~**ANS-002**~~ |
| Inventory files                                                     | 🟡 `path`, `format` only            | Recognized by well-known name or an `inventory/`/`inventories/` ancestor; **contents never parsed**                                              |             |
| Inventory groups / hosts inside those files                         | ⚪                                  | Deliberate: a **View** _is_ the group (ADR-0055 G3) and hosts come from their own Syncers, never a writable CMDB (§1.2)                          |             |
| `group_vars/`, `host_vars/`                                         | 🟢 scope, target, **key names**     | `ansible.varscope`. Values are NEVER projected (§2.5) — a vars file routinely holds credentials in the clear. Both the file and directory forms; the directory form unions its files, as ansible does. **Precedence is observed, never computed**: two scopes binding one name both project, neither marked a winner | ~~**ANS-003**~~ |
| `ansible.cfg`                                                       | 🟢 allowlisted values + other key **names** | It changes the meaning of everything else in the root, so observing it also FIXED the role reader: `roles_path` is now honored, not just recorded. `[galaxy_server.*] token` is why values are allowlisted (§2.5) | ~~**ANS-005**~~ |
| `library/`, `module_utils/`, `filter_plugins/`, `callback_plugins/` | 🟢 name, type, path, owning role | `ansible.plugin`, in BOTH layouts (classic `library/` and collection `plugins/modules/`). Contents never read — that is a program, not structure                | ~~**ANS-006**~~ |
| `galaxy.yml` (the root **is** a collection)                         | 🟢 fqcn, version, deps, license | Same `ansible.collection` Kind as a required one, marked `root` — one question, one Kind. Its own `dependencies` live here, not in requirements.yml            | ~~**ANS-007**~~ |
| Vaulted files (`$ANSIBLE_VAULT` header)                             | 🟢 `vaulted: true`, no keys         | Exactly as this row asked: present and vaulted, never decrypted. An empty key list WITH `vaulted:true` distinguishes "binds nothing" from "binds things I cannot show you" (§1.8) | ~~**ANS-008**~~ |
| `molecule/`, `.yamllint`, `meta/runtime.yml`                        | ⚪                                  | Test scaffolding and lint config; not estate                                                                                                     |             |
| Multi-document YAML playbooks                                       | 🟠                                  | `playbookPlays` unmarshals a single document; a `---`-separated multi-doc playbook would project only its first doc. Legal but rare              | **ANS-009** |

**On the ⚪ rows.** Declining to parse inventory contents and role internals is the §9 line, correctly
held. **ANS-003** and **ANS-004** were on the other side of it — a `group_vars/` file's _existence and
scope_ and a role's _declared dependencies_ are structure, not execution semantics — and both shipped
on 2026-07-31 with that boundary intact: role tasks/handlers/defaults are still unread, and variable
PRECEDENCE is still ansible's to decide. Two scopes binding one name both project with neither marked
a winner; computing the winner would reinterpret the execution model (§9) and would have Stratt assert
a fact about a run that has not happened (§1.2).

---

## 2. Execution surface

What `ansible-playbook` can do, against what a Step can ask for
([shim.go](../../plugins/ansible/shim.go),
[ansible.input.v6](../../contracts/actuators/ansible.input.v6.schema.json)).

**Supported and typed** 🟢 — `play` · `scm{repo,ref,playbook}` · `extraVars` ·
`become{enabled,user,method}` · `limit` · `tags` · `skipTags` · `forks` · `diff` · `verbosity` ·
`timeout` · `vault{credentialRef,file}` · `connection{user,credentialRef,file,hostKeyChecking,jump}`.
Check mode rides the port's `DryRun` bit rather than a param (ADR-0117 D2); the EE image is the
Actuator's declaration rather than a param (D3a). Facts return as governed Facets; per-target results and
slicing are `RunOutcome.PerTarget`.

Each knob is a **typed field rendered as its own argv token**
([shim.go:148-188](../../plugins/ansible/shim.go#L148-L188)) — never a free-form flag string. That is
what keeps this an argument surface instead of an injection one, and it is genuinely ahead of the AWX
mirror of the same fields.

| `ansible-playbook` capability                              | Status | Note                                                                                                                                                                         | ID          |
| ---------------------------------------------------------- | ------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------- |
| `--connection` / `ansible_connection` other than ssh+local | 🟡     | **`network_cli` + `netconf` ship (ADR-0153, 2026-07-31)** — the netcommon reach. `winrm`/`psrp` and `httpapi` are still absent and are REFUSED by the enum rather than accepted; see §3 | **ANS-001** |
| SSH **password** auth (`ansible_password`)                 | 🟢     | ~~key-only~~ — **`connection.passwordRef` ships (ADR-0153 D3)**, rendered as `--connection-password-file`: a PATH, never a value                                              | **ANS-001** |
| **become password** (`ansible_become_password`)            | 🟢     | ~~cannot escalate~~ — **`become.passwordRef` ships (ADR-0153 D5)** → `--become-password-file`. A ref without `enabled: true` is refused                                       | ~~**ANS-010**~~ |
| `--vault-id` (multiple vault identities)                   | 🟢     | ~~one only~~ — **`vault` takes an object OR a list, each with an optional `id` (ADR-0153 D4)**. Duplicate ids refused                                                         | ~~**ANS-011**~~ |
| `--start-at-task`, `--step`                                | 🔴     | Interactive/partial execution. Low value for a governed platform ⚪-adjacent                                                                                                 | **ANS-012** |
| `--syntax-check`, `--list-tasks`, `--list-hosts`           | 🔴     | Cheap, and genuinely useful as a pre-flight gate before a Run                                                                                                                | **ANS-013** |
| `--force-handlers`, `--flush-cache`                        | 🔴     | Low value                                                                                                                                                                    | **ANS-012** |
| Strategy plugins (`free`, `host_pinned`), `serial`         | ⚪     | Authored **in the play**, not on the CLI — correctly out of the Actuator's surface                                                                                           |             |
| `--ssh-common-args` / `--ssh-extra-args`                   | ⚪     | Deliberately not exposed: the shim **composes** these itself for host-key policy and ProxyJump. Exposing a raw string is exactly the injection seam the typed design removes |             |
| Dynamic inventory plugins at run time                      | ⚪     | A View resolves targets; a run-time inventory plugin would be a second source of truth (§1.2)                                                                                |             |
| Ad-hoc module runs (`ansible` not `ansible-playbook`)      | ⚪     | A Run against a View is the equivalent; AWX's `ad_hoc_commands` is likewise unmirrored                                                                                       |             |
| `--module-path`                                            | 🔴     | Tied to **ANS-006** — no way to point at a repo's own modules                                                                                                                | **ANS-006** |
| Async / `poll`                                             | ⚪     | Play-level; the Run is the async unit here                                                                                                                                   |             |
| Retry files / `--limit @retry`                             | ⚪     | Re-running a Run against the failed subset is a platform concern, not a flag to plumb                                                                                        |             |

---

## 3. The connection-type gap · ANS-001 — mostly closed (ADR-0153), Windows outstanding

**Before ADR-0153.** `connectionVars` emitted exactly three things: `ansible_user`,
`ansible_ssh_private_key_file`, and `ansible_ssh_common_args`. The only other connection form anywhere in
the path was `ansible_connection=local`, from the reserved `local` value of `mgmt.address`. So the
Actuator reached **Linux/Unix over SSH with a private key**, and the control node — and that was a reach
gap, not a depth gap: for a network fleet or a password-only estate the answer was "you cannot use
Stratt," and no document said so.

The tell that it was never a decision: the `mgmt.address` Facet schema describes itself as feeding "the
ansible/ssh/**winrm** Actuator" — the coordinate was designed for a connection type the execution path
never grew.

**What ships now** (`ansible.input.v8`,
[ADR-0153](../adr/0153-a-connection-type-and-a-password-that-is-only-ever-a-path.md)):

| Reach                                      | Then | Now                                                                  |
| ------------------------------------------ | ---- | -------------------------------------------------------------------- |
| Linux/Unix over SSH **with a key**         | ✅   | ✅ unchanged — an ssh Step renders byte-identically to v7             |
| Any SSH host **without** a key             | ❌   | ✅ `connection.passwordRef`                                           |
| **Network devices** (`ansible.netcommon`)  | ❌   | ✅ `connection.type: network_cli` / `netconf` + `connection.networkOS` |
| Escalation that **prompts** for a password | ❌   | ✅ `become.passwordRef`                                               |
| A repo with **two vault identities**       | ❌   | ✅ `vault` as a list with `id`s                                       |
| **Windows** (`winrm`/`psrp`)               | ❌   | ❌ **still no** — and the enum REFUSES the value                      |
| `httpapi`                                  | ❌   | ❌ its own decision (needs use_ssl/validate_certs/port)               |
| Containers (`docker`/`podman`/`kubectl`)   | ❌   | ❌ nobody has asked                                                   |

**The credential half was the design, and the mechanism is worth knowing.** All three secrets reach
ansible as a **file path** — `--connection-password-file`, `--become-password-file`,
`--vault-password-file` / `--vault-id id@path` — verified against the ansible-core the EE pins. So a
password is never a value anywhere. The shape everybody writes first, `ansible_password` as an inventory
group var, is not a weaker option but a forbidden one: `writeInventory` creates `inventory/hosts` at
**0644** in the private data dir **beside `artifacts/`**, and §2.5 says material is never written to
artifacts.

**Two refusals rather than resolutions.** `connection.networkOS` is required for the netcommon types and
refused for ssh — a guessed vendor connects and then issues another vendor's syntax, which surfaces as a
play failure pointing at the wrong thing. And a non-ssh `type` on a run that includes a `local` target is
refused outright: `local` is a HOST var, host vars beat group vars, so the local target would silently
connect a different way — implicit precedence hiding inside two declarations that each look right (§2.4).

**Windows is the honest residue.** It is the most-asked-for row in the register and it is absent because
it cannot be verified: there is no freely-runnable Windows target in CI, so shipping `winrm` would ship a
code path nothing had ever executed. The enum therefore **rejects** it at estate load with a message
naming the gap, instead of accepting it and failing on a fleet someone already migrated. When a target
can be stood up and driven end to end, `winrm` is one enum value plus a credential form that now exists.

**The enum was not enough, and verifying it is what found that out.** `network_cli` and `netconf` are
**not in ansible-core** — measured, not assumed:

```
$ docker run --rm stratt-ee:dev ansible-doc -t connection network_cli
[WARNING]: Error loading plugin 'ansible.netcommon.network_cli':
           No module named 'ansible_collections.ansible.netcommon'
```

So a Contract that accepted the value on the default EE would pass review, pass the estate load, pass
every unit test — and die at connect time naming a python module the estate never wrote. Which is
verbatim the failure `platform.requirements.yml` was written about for `community.general.apk`. ADR-0153
D7 closes it in both directions:

| Image                             | plugin resolves?                          | shim behaviour                        |
| --------------------------------- | ----------------------------------------- | ------------------------------------- |
| `stratt-ee:dev` (platform floor)  | ❌ `network_cli` not found                 | **refuses**, naming `ansible.netcommon` and where to add it |
| `stratt-ee-network:dev` (variant) | ✅ `network_cli` + `netconf` both resolve  | allows                                |

The shim reads the image's own run-visible content manifest (`/etc/stratt/ee-content.json`) rather than
probing — `ansible-doc` **exits 0 for a plugin that does not exist**, also measured, so the obvious
probe silently passes. Declaration errors are still reported before image errors, so nobody is sent to
rebuild an EE over a typo.

`ee/content/network.requirements.yml` ships **`ansible.netcommon` only** — the connection plugins, which
is the mechanism the enum promises. Vendor collections (`cisco.ios`, `arista.eos`) are deliberately not
included: which vendors a fleet needs is exactly the adopter-shaped question an EE variant exists for
(ADR-0117 D3). An adopter copies the file, adds their vendors, relocks, and selects the image from their
own Actuator.

### The substrate transports — and the assumption that hid them (ADR-0156)

The connection audit above was written on an assumption nobody had checked: that reaching a managed
host means SSH on port 22. **Measuring the collections falsified it.** All three substrates ship a
native connection plugin and none of them uses SSH:

| Transport                       | Reaches the guest via              | Guest needs                       | Status |
| ------------------------------- | ---------------------------------- | --------------------------------- | ------ |
| `kubernetes.core.kubectl`       | `kubectl exec`                     | **nothing at all**                | 🟢 **LIVE-PROVEN** on kind — a pod asserted to have no sshd, no ssh client and nothing on port 22, converged over kubectl |
| `community.vmware.vmware_tools` | vCenter guest ops — **no network path to the guest** | VMware Tools + guest creds | 🟡 shipped, unit-tested, **not live-proven**: vspheresim implements the vCenter API but not Tools guest operations |
| `amazon.aws.aws_ssm`            | an AWS SSM Session                 | SSM Agent, instance profile, curl | 🟠 the shim supports it; **nothing produces it yet** — see below |

**The transport is OBSERVED, not declared** (ADR-0156 D1): the Syncer that saw the host says how to
reach it, in a `mgmt.transport` Facet beside `mgmt.address`. Declaring it on the Step would force
every converge Workflow to name a substrate — the thing ADR-0151 removed from every declaration
above the provider — and would make a mixed-substrate View unconvergeable, because connection
settings render as inventory GROUP vars, one value per Run. Rendered per HOST, one Assignment
converges pods, VMs and EC2 instances in **one Run**, naming none of them.

**`aws_ssm` has no writer, and that is deliberate rather than unfinished.** The awsec2 Syncer can
honestly observe *neither* EC2 path. `KeyName` means a key is AUTHORIZED, not that sshd is listening
— inferring reachability from it is COMPUTING a reach fact, which ADR-0142 D4 forbids in those
words, and floci proves the gap is real rather than pedantic (its instances carry a KeyName and ship
no sshd). SSM is authoritatively answerable — `DescribeInstanceInformation` lists exactly the
instances whose agent registered — but that is a different AWS API with its own IAM scope. **Booked:
the SSM client and the transport land together or not at all**, since a Facet has no other writer.

**What the kubectl proof retires:** `kubecompute` had to bake sshd and authorized keys into every pod
it built, purely because the connection method had been assumed. That coupling is now removable.

---

**What is still outstanding is the DEVICE half.** A collection that installs is not a connection that
works: **no real device has been driven end to end from this repo**. That needs a CI-runnable target (an
FRR or cEOS container), and it is booked in the same shape as PLG-1's bastion half. A unit-green,
image-verified connection type is still not a proven one.

---

## Prioritized gaps

**Tier 1 — bounds what estates Stratt can manage at all:**

- **ANS-001 · Connection types + non-key credentials.** ~~Windows and network devices.~~ **Mostly done
  (ADR-0153, 2026-07-31)**: `network_cli`/`netconf` + all three credential forms. Two pieces remain, and
  neither is a flag — **(a) Windows**, blocked on a verifiable target rather than on design; **(b) a live
  network-device run**, blocked on a CI-runnable device container (FRR/cEOS). The COLLECTION half of (b)
  is now done and image-verified both ways (`ee/content/network.requirements.yml`, ADR-0153 D7); what
  remains is driving a real device.
- ~~**ANS-010 · Become password.**~~ **Done (ADR-0153 D5)** — `--become-password-file`, and a password
  supplied without `enabled: true` is refused rather than silently ignored.

**Tier 2 — the estate cannot see where configuration comes from:**

~~All three done (2026-07-31), as one batch — they are one mechanism in one place:~~

- ~~**ANS-003 · `group_vars/` / `host_vars/` observation.**~~ `ansible.varscope`: scope, target and
  **key names, never values** (§2.5). Carried **ANS-008** with it — a vaulted file is present and
  vaulted, never decrypted.
- ~~**ANS-004 · Role `meta/` dependencies.**~~ `depends-on` relations + galaxy_info provenance.
- ~~**ANS-002 · `requirements.yml` roles half.**~~ Same `ansible.role` Kind, marked `required`.

**Tier 3 — completeness of the content picture:**

~~**ANS-006** custom modules/plugins · **ANS-005** `ansible.cfg` · **ANS-007** collection-shaped
roots~~ — **all done (2026-07-31)**, as one batch with the same mechanism as Tier 2. `ansible.cfg`
was the one with teeth: observing it exposed that the role reader had been searching `roles/`
unconditionally, so a root configured with `roles_path = galaxy_roles` projected **zero roles and
reported no problem** — fixed, not merely recorded. Also ~~**ANS-011** multi-identity vault~~ (done,
ADR-0153 D4). Still open: **ANS-013** pre-flight syntax check (a Step-level gate, a different
mechanism from these projections) and `--module-path` on the execution surface.

**Unexamined (🟠) — look before deciding:** **ANS-009** multi-document playbooks.

**Explicitly not gaps:** strategy/serial (play-level), raw ssh args (the injection seam the typed design
exists to remove), run-time dynamic inventory (a second truth), ad-hoc commands, retry files.
