# ADR 0149 — The execution environment carries a content floor, and every variant is a superset

- **Status:** **Proposed** (2026-07-29, steward) — implemented, **live-proven, and falsified**.
  Charter review by hand (this session's rules bar the subagent); §1.1/§1.4/§1.5/§1.8/§2.4/§3/§7.3
  answered inline. **No new dependency, no new Named Kind, no schema change, no migration** — one
  declaration file, one build-arg default, one check.
- **Date:** 2026-07-29
- **Deciders:** steward
- **Charter sections:** §7.3 (pinned executable content — the floor is pinned and byte-locked like
  everything else), §1.5 (content is data, pinned and hash-verified; the core never learns it),
  §1.8 (the failure this fixes was a diagnostic that named a collection the author never wrote),
  §2.4 (a guarantee that resolves differently depending on which image a Step selected is implicit
  precedence, refused), §1.4 (no new machinery — the existing pin/lock pipeline, one more file),
  §3 (Ansible and its collections are GPL content in a subprocess image; unchanged posture)
- **Reconciles with:** **ADR-0117 D3/D3a** (content is a build-time declaration; the image is the
  content boundary; a Step selects content by selecting an Actuator — this ADR supplies the rule D3
  left implicit, that _some_ content is not optional), **ADR-0117 follow-up (i)** (the per-artifact
  lockfile, whose one-file-per-image consequence is why the floor is repeated rather than composed),
  **ADR-0124 D1/D2** (the `execution-environment.yml` front door and the offline artifact source —
  both unchanged; the floor is an ordinary declaration on both paths), **ADR-0121 D4** (the
  `kind=ee-content` `SCOPE_RUN` event, which is how a Run states the floor it actually had),
  **ADR-0134** (tool content lives beside the estate — this is EE content, a different tier),
  **ADR-0148** (the apache/tomcat/nginx content roots whose execution found this)

## Context

The default EE was built with `EE_CONTENT=""` and shipped **zero Ansible collections**.

That was never a decision. It was the Dockerfile's `ARG` default, which means the platform's own
content set was an empty string in a build file: not reviewable, not pinned, not locked, not
citable, and chosen by nobody. Only `stratt-ee-crypto` had a declaration, because someone had a
concrete need (`community.crypto` for the certificate demo) and wrote it down. Absence of a need
nobody had articulated produced absence of a file.

**What it cost, measured.** `ansible.builtin.package` is a **dispatcher, not an implementation**.
`apt` and `yum`/`dnf` are in ansible-core; `apk`, `zypper`, `pacman`, `pkgng` and `portage` are
**not** — they live in `community.general`. So the shipped apache play installed cleanly on Debian
and failed on Alpine:

```
[WARNING]: Error loading plugin 'community.general.apk': No module named 'ansible_collections.community'
[ERROR]: Task failed: Could not find a matching action for the "apk" package manager.
```

Stratt's default execution environment could not install a package on an Alpine host at all.

**Why nothing caught it, which is the part worth keeping.** Every shipped content root under
`plugins/*/estate/content/` uses `ansible.builtin.*` **exclusively** — not one collection FQCN
appears anywhere. A static scan for required collections therefore finds nothing to require, and
would have pronounced the empty EE adequate. The dependency is created at run time by ansible's own
dispatch on the **target's** distro, and is invisible in the source that depends on it. The estate
loads, the content ref resolves, the playvars guard passes, `task ci` is green, and the thing does
not work. It was found by **running** the content ([`task dev:content:proof`](../../Taskfile.yml)),
which is the only way it could have been found.

The diagnostic is also actively misleading (§1.8): the module is named `ansible.builtin.package`, so
the error points at a collection the play's author never mentioned and would not think to declare.

### What already ships that this must reconcile with

| Machinery                                                                | Where                              | Bearing on this decision                                       |
| ------------------------------------------------------------------------ | ---------------------------------- | -------------------------------------------------------------- |
| Content is a **build-time** declaration; the image digest is the truth   | ADR-0117 D3                        | The floor is a declaration like any other — nothing new        |
| Exact pins asserted **before** the network is touched                    | `content.py:verify`                | The floor is pinned by the same rule                           |
| Per-artifact SHA-256 **lockfile** over the installed tree                | ADR-0117 (i)                       | The floor is byte-locked; a floor that wasn't would be theatre |
| **One requirements file per image**, refused otherwise                   | `content.py:install` (`len > 1`)   | **The whole shape of D2** — a variant cannot compose the floor |
| Content is selected by selecting an **Actuator**, never a run-time param | ADR-0117 D3/D3a                    | Unchanged; the floor is what a Step gets without selecting     |
| A variant that is silently WEAKER looks identical from the estate        | ADR-0117 D3a (the facet-grant bug) | The precedent D2 is defending against, in a second dimension   |
| `EE_CONTENT` / `EE_LOCK_MODE` / `EE_OFFLINE` build args                  | `ee/Dockerfile`                    | Where the fix lands: a default, not a new mechanism            |

## Decision

### D1 — The platform declares a content **floor**, and it is what the platform's own content requires

`ee/content/platform.requirements.yml` is an ordinary Galaxy requirements file, pinned and locked
like every other, and `EE_CONTENT` **defaults to it**. Today it carries `community.general` — the
other half of `ansible.builtin.package` and `ansible.builtin.package_facts`, both of which dispatch
on the target's package manager and both of which leave ansible-core the moment the target is not
apt- or yum-based.

**The membership rule is the decision, not the current contents.** The floor carries exactly what
**the platform's own shipped content** requires — what is needed to execute
`plugins/*/estate/content/` against the distros the platform claims to manage. It does **not** carry
what an adopter's content might want.

Two properties follow, and both are why the rule is worth stating rather than leaving to taste:

- **It is bounded and checkable.** "What our content needs" can be audited against a directory. "What
  users will want" cannot, and a floor defined that way grows monotonically forever, because no
  entry ever has a falsifiable reason to leave.
- **It does not pre-empt the variant seam.** ADR-0117 D3 already answers "content my estate needs":
  declare an EE variant and select it with an Actuator. A floor that tried to be a catalogue would
  make that seam vestigial while still never being complete.

**A contentless image stays expressible** — `--build-arg EE_CONTENT=` — and still writes its
manifest, so "no content" remains available as a **deliberate** choice rather than as what you get
by not deciding. That asymmetry is the fix. The defect was never that zero was the wrong number; it
was that zero was not an answer to a question anyone had asked.

### D2 — Every variant is a **superset** of the floor, at the same versions, and it is enforced

A variant cannot compose the floor, because `install` refuses more than one requirements file per
image — deliberately, and for a good reason: a lockfile records the installed **closure** (the
platform lock proves it, carrying `community.library_inventory_filtering_v1` as a transitive
dependency), and a closure cannot be attributed to one of several declarations. A file that read
"these are crypto's artifacts" while listing someone else's would be worse than the duplication.

So a variant **repeats** the floor, and `ee/content.py verify` — hence `task ci` — fails if any
variant drops a floor entry or pins it at a different version. Duplication that nothing checks is
duplication that drifts; the check is what makes repeating it honest.

**Why the property matters.** A variant exists to **add** capability. If selecting it also subtracted
capability, a Step that asked for `stratt-ee-crypto` because it needs openssl modules would
_silently_ also be asking for an environment that can no longer install a package on Alpine — a
strictly weaker environment that is indistinguishable from the estate. That is exactly the failure
ADR-0117 D3a already hit once, where a declared ansible variant ran and then had every fact
write-back refused because its grant was narrower and nothing said so. Same shape, different axis.

**Why versions must match rather than merely be present.** A floor whose content depends on which
image a Step happened to select is a fleet with two answers to "can this platform install a package
on Alpine." That is §2.4's implicit-precedence hazard wearing a supply-chain hat, and §2.4's rule is
that the conflict is refused, never tie-broken.

### D3 — The floor is a floor, not a catalogue, and the Hub gap stays open

Stated because a floor invites more trust than it earns. This decision does **not** close the
Automation Hub gap: we still ship no content **registry**, no discovery, no curation, and no answer
to "which collections should my estate use." An adopter's content will assume a collection set, and
the floor answers that question only for the collections the platform itself needs. The rest is W7,
and it remains 🔴.

What the floor does close is narrower and worth having on its own: **any Stratt EE can install a
package on any distro the platform's own content targets**, and that sentence is now pinned,
byte-locked, checked in CI, and proven by execution.

## Alternatives considered

- **Leave the default empty; declare a variant per need.** The purist reading of ADR-0117 D3, and
  rejected on two counts. It makes `ansible.builtin.package` — the module ansible itself offers as
  _the_ distro-agnostic abstraction — work on some targets and not others, with a diagnostic that
  names a collection the author never wrote. And because variants cannot compose, it is
  combinatorial: apache-on-Alpine plus a certificate needs a _third_ image declaring both.
- **Put the distro→package mapping in the Intent instead.** Rejected in ADR-0148 already and for the
  same reason here: it makes core learn distro packaging (§1.1/§9 ontology creep) and is the
  env-keyed-values shape ADR-0118 D1 refuses, rotated one axis. The right home for "what is this
  package called here" is content, and content's right to answer it is precisely what the floor buys.
- **Amend `ansible.builtin.package` out of the shipped content** (use `apk`/`apt`/`yum` explicitly
  with a `when:`). Rejected: it hard-codes into every play the dispatch ansible already implements,
  and it would leave the platform EE unable to run ordinary community content anyway.
- **Make the floor a superset-by-inheritance in the requirements format.** Rejected: it forks the
  Galaxy format (§1.1 — we consume the real one, and ADR-0124 D1's compatibility claim depends on
  that), and it would break the one-lockfile-per-closure property that makes the lock meaningful.

## Charter alignment

- **§7.3 / §1.5.** The floor is exactly-pinned, and byte-pinned by
  `platform.requirements.lock.json`. It is content, verified as data — the core learns nothing.
- **§1.4.** No new machinery: one declaration file, one changed `ARG` default, one check inside the
  gate that already runs. No new dependency, no second build path.
- **§1.8.** The failure being fixed is a diagnostic problem as much as a capability one. A Run still
  states the content it had on its own event stream (`kind=ee-content`, ADR-0121 D4), so the floor
  is legible during descent rather than assumed.
- **§2.4.** Version divergence between the floor and a variant is refused, not reconciled.
- **§3.** `community.general` is GPL-3.0-or-later and ships in the EE image — the same subprocess
  boundary ansible-core already sits on. The Go control plane links neither. No new posture.
- **§1.7.** Pinned at the current release; the floor moves on the evergreen train like any pin, and
  moving it means relocking every variant, which the check makes impossible to forget.

## Consequences

- **Positive.** The platform can install a package on Alpine (and SUSE/Arch/FreeBSD) — proven. The
  platform's own content set is a reviewable file with a lockfile and a reason, instead of an empty
  build arg. A variant can no longer be silently weaker than the default.
- **Negative / accepted.** The floor is duplicated into every variant, and bumping it means relocking
  each — the price of one-lockfile-per-closure, paid deliberately and guarded by the check. The
  default EE is larger. And the floor is now a **contract**: an adopter's content will come to assume
  it, so removing an entry later is a breaking change, which is why the membership rule in D1 is
  narrow on purpose.
- **Neutral.** Nothing about run-time content resolution changes; ADR-0124's offline path verifies
  the floor by the identical hash it verifies everything else by.

## Verification — the evidence, including the falsification

- **`task dev:content:proof` green (2026-07-29).** The shipped apache play installs `apache2` on
  `alpine:3.22` via apk, and the tomcat play installs `tomcat10` on `debian:12-slim` via apt, from
  Intents naming no package manager. **Each node is asserted bare first**, so a green run means the
  remediation installed the package rather than the image having shipped it.
- **Falsified.** The same proof, re-pointed with `STRATT_LIVE_EE_IMAGE` at an EE rebuilt with
  `--build-arg EE_CONTENT=`, fails on apache with the original
  `Could not find a matching action for the "apk" package manager`. Only the EE's content differs,
  so the floor is demonstrably what fixed it.
- **D2 falsified three ways** (`ee/content_test.py`): a variant that drops a floor entry, one that
  re-pins it at another version, and an absent floor file are each refused with the fix named.
- **The lock is real.** `community.general` 13.2.0 resolved to the identical tree digest in two
  independent builds (the platform lock and the crypto relock), which is the property the lockfile
  exists to make checkable.

## Follow-ups

- **(a) A second package-manager family is still unexecuted.** The apache play's
  `'httpd' if ansible_os_family == 'RedHat' else 'apache2'` has three outcomes and two have now run.
  A RedHat-family node in `dev:content:proof` would close the third; until then the `httpd` branch is
  declared and unproven, and this ADR says so rather than implying a matrix it does not have.
- **(b) The floor has no automatic tie to the content that justifies it.** D1's membership rule is
  auditable by hand against `plugins/*/estate/content/` but is not machine-checked, and it cannot be
  by static means (that is the whole finding). The honest guard remains execution — which is an
  argument for `dev:content:proof` growing targets, not for a scanner that would give false comfort.
- **(c) Air-gap re-verification.** `task ee:content:pull` + `EE_OFFLINE` should be exercised against
  the floor at least once; ADR-0124 D2's guarantee is unchanged in principle and unproven for this
  declaration in practice.
