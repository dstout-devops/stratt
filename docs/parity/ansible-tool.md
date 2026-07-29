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
is **ANS-001**: the Actuator speaks SSH with a private key and nothing else, so a Windows estate or a
network fleet is not partially supported, it is unsupported. That is a bigger hole than anything in the
[AWX object-model audit](awx-object-model.md), and unlike those, it is not visible from any existing doc.

---

## 1. Content-root shape

What an Ansible project contains, against what the content half projects
([content/types.go](../../plugins/ansible-automation/content/types.go),
[content/normalize.go](../../plugins/ansible-automation/content/normalize.go)).

| Content-root element                                                | Observed?                           | Note                                                                                                                                             | ID          |
| ------------------------------------------------------------------- | ----------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------ | ----------- |
| Playbooks (any `.yml` that is a play sequence)                      | 🟢 `name`, `path`, `plays`, `hosts` | Detected by **shape**, not location — a top-level sequence of mappings with `hosts:` or `import_playbook:`. Pinned schema                        |             |
| `roles/*/`                                                          | 🟡 `name`, `path` only              | A role is a name and a directory; nothing inside it is read                                                                                      |             |
| `roles/*/meta/main.yml` → `dependencies`                            | 🔴                                  | **Role→role dependency is graph-shaped structure**, and it is the one thing inside a role worth projecting                                       | **ANS-004** |
| `roles/*/meta/main.yml` → `galaxy_info`                             | 🔴                                  | Author, license, supported platforms — cheap metadata once `meta/` is being read at all                                                          | **ANS-004** |
| `collections/requirements.yml` → `collections:`                     | 🟢 `name`, `version`, `source`      | Both Galaxy-legal forms (bare FQCN string and mapping) handled                                                                                   |             |
| `requirements.yml` → `roles:`                                       | 🔴                                  | The **roles half is not parsed** — only `collections:`. Already booked by ADR-0127 D4                                                            | **ANS-002** |
| Inventory files                                                     | 🟡 `path`, `format` only            | Recognized by well-known name or an `inventory/`/`inventories/` ancestor; **contents never parsed**                                              |             |
| Inventory groups / hosts inside those files                         | ⚪                                  | Deliberate: a **View** _is_ the group (ADR-0055 G3) and hosts come from their own Syncers, never a writable CMDB (§1.2)                          |             |
| `group_vars/`, `host_vars/`                                         | 🔴                                  | Where an Ansible estate's actual configuration lives. Not observing them means "why did this host get this value" is unanswerable from the graph | **ANS-003** |
| `ansible.cfg`                                                       | 🔴                                  | Sets roles paths, connection defaults, strategy, callbacks — it changes the meaning of everything else in the root                               | **ANS-005** |
| `library/`, `module_utils/`, `filter_plugins/`, `callback_plugins/` | 🔴                                  | A repo's own custom content is invisible; on a migration this is the content most likely to break                                                | **ANS-006** |
| `galaxy.yml` (the root **is** a collection)                         | 🔴                                  | A collection-shaped repo is not recognized as one — it projects as loose playbooks and roles                                                     | **ANS-007** |
| Vaulted files (`$ANSIBLE_VAULT` header)                             | 🟠                                  | Unexamined. A vaulted `group_vars/all.yml` should be observed as _present and vaulted_, never decrypted (§2.5)                                   | **ANS-008** |
| `molecule/`, `.yamllint`, `meta/runtime.yml`                        | ⚪                                  | Test scaffolding and lint config; not estate                                                                                                     |             |
| Multi-document YAML playbooks                                       | 🟠                                  | `playbookPlays` unmarshals a single document; a `---`-separated multi-doc playbook would project only its first doc. Legal but rare              | **ANS-009** |

**On the ⚪ rows.** Declining to parse inventory contents and role internals is the §9 line, correctly
held. **ANS-003** and **ANS-004** are on the other side of it: a `group_vars/` file's _existence and
scope_ and a role's _declared dependencies_ are structure, not execution semantics — observing them
reinterprets nothing.

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
| `--connection` / `ansible_connection` other than ssh+local | 🔴     | See §3 — the finding of this audit                                                                                                                                           | **ANS-001** |
| SSH **password** auth (`ansible_password`)                 | 🔴     | `connectionVars` renders `ansible_ssh_private_key_file` and nothing else — key-only                                                                                          | **ANS-001** |
| **become password** (`ansible_become_password`)            | 🔴     | `becomeParams` is `{enabled,user,method}`; a sudo-with-password target cannot escalate                                                                                       | **ANS-010** |
| `--vault-id` (multiple vault identities)                   | 🟡     | One `vault.credentialRef` only; a repo using two vault ids cannot run                                                                                                        | **ANS-011** |
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

## 3. The connection-type gap · ANS-001

`connectionVars` ([connection.go:54-102](../../plugins/ansible/connection.go#L54-L102)) emits exactly
three things: `ansible_user`, `ansible_ssh_private_key_file`, and `ansible_ssh_common_args`. The only
other connection form anywhere in the path is `ansible_connection=local`, from the reserved `local` value
of `mgmt.address` ([ansible.go:88](../../plugins/ansible/ansible.go#L88)).

So the Actuator can reach: **Linux/Unix over SSH with a private key**, and the control node itself. It
cannot reach:

- **Windows** — `winrm` / `psrp`. No connection type, and no password auth, which Windows needs.
- **Network devices** — `network_cli`, `netconf`, `httpapi`. The `ansible.netcommon` family is a large
  part of why enterprises buy AAP.
- **Containers** — `docker`, `podman`, `kubectl` connection plugins.
- **Any SSH host that does not take a key** — password-only estates, which legacy fleets still are.

This is not a depth gap, it is a reach gap: for those estates the answer today is "you cannot use Stratt,"
and no current document says so. Note the tell that it was never a decision — the `mgmt.address` Facet
schema already describes itself as feeding "the ansible/ssh/**winrm** Actuator", so the coordinate was
designed for a connection type the execution path never grew.

Worth stating clearly because it bounds the fix: the **graph side is ready**. `mgmt.address` carries
address + port and is deliberately closed against growing into a device ontology (§9), so adding a
connection type is an Actuator-and-Contract change — an `ansible.input.v7` with a typed `connection.type`
enum plus the credential forms each type needs — not a data-model change. It should be its own ADR, and
the credential half (passwords, brokered per §2.5, never in `extraVars`) is the part that needs the
argument.

---

## Prioritized gaps

**Tier 1 — bounds what estates Stratt can manage at all:**

- **ANS-001 · Connection types + non-key credentials.** Windows and network devices. Own ADR; the
  credential-form half is the design work, not the flag.
- **ANS-010 · Become password.** Small, and blocks a common sudo posture. Naturally batched with
  ANS-001, since both are "a credential form the connection surface has no shape for."

**Tier 2 — the estate cannot see where configuration comes from:**

- **ANS-003 · `group_vars/` / `host_vars/` observation.** Structure, not semantics: which files exist and
  what scope they bind to. This is what makes "why does this host have this value" answerable.
- **ANS-004 · Role `meta/` dependencies.** Role→role edges are the one graph inside a role.
- **ANS-002 · `requirements.yml` roles half.** Already booked by ADR-0127 D4.

**Tier 3 — completeness of the content picture:**

- **ANS-006** custom modules/plugins (+ `--module-path`) · **ANS-005** `ansible.cfg` ·
  **ANS-007** collection-shaped roots · **ANS-011** multi-identity vault ·
  **ANS-013** pre-flight syntax check.

**Unexamined (🟠) — look before deciding:** **ANS-008** vaulted-file observation ·
**ANS-009** multi-document playbooks.

**Explicitly not gaps:** strategy/serial (play-level), raw ssh args (the injection seam the typed design
exists to remove), run-time dynamic inventory (a second truth), ad-hoc commands, retry files.
