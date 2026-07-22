---
name: github-actions-hardening
description: >-
  Security-hardening reviewer for GitHub Actions workflow files (.github/workflows/*.yml). Reasons
  about the Actions threat model that pattern matchers and general linters miss — untrusted-input
  script injection, privileged triggers running fork code, mutable action references, and
  over-scoped GITHUB_TOKEN. Use when asked to review, audit, harden, or secure a workflow, when
  authoring a new one, or for "is this workflow safe?", "review my CI for security", "why is
  pull_request_target dangerous here?", "pin my actions", "lock down GITHUB_TOKEN". Covers ${{ }}
  script injection, pull_request_target / workflow_run privilege escalation, SHA-pinning, least
  privilege, GITHUB_ENV/GITHUB_OUTPUT injection, secret exposure, OIDC/keyless signing, self-hosted
  runner exposure. Adapted for Stratt from github/awesome-copilot (charter §7.3 supply-chain).
---

# GitHub Actions Hardening (Stratt)

A focused security reviewer for the workflows under `.github/workflows/` (Stratt's CI gate is
`ci.yml` → `task ci`; there are also image-build/sign and release paths). It reasons about the
*Actions-specific* threat model — where trust boundaries live in trigger types, token scopes, and
string interpolation — not the application-code bugs a general scanner finds. Most workflow risk is
invisible to language linters because the dangerous code is the YAML itself and the way GitHub
expands `${{ }}` into a shell before your script runs.

**Charter tie-in:** §7.3 (signed releases, SLSA, SBOM, pinned dependencies) and §1.5 (schema/artifact
integrity) make CI a load-bearing supply-chain surface. This skill is the *reviewer* those policies
lacked. It complements `/security-review` (application code), it does not duplicate it.

## When to use

- Reviewing, auditing, or hardening any file under `.github/workflows/`.
- Authoring a new workflow and wanting it secure by default.
- Any workflow using `pull_request_target`, `workflow_run`, `issue_comment`, or `issues`.
- Questions about `GITHUB_TOKEN` / the `permissions:` key.
- Pinning actions to commit SHAs vs tags vs branches.
- Handling untrusted input (issue/PR titles, bodies, branch names, commit messages) in `run:` steps.
- OIDC / keyless (cosign) signing and secret handling in CI.

## The core insight

In a workflow, **`${{ <expr> }}` is expanded by the runner into the script *before* the shell
executes it.** So:

```yaml
- run: echo "Title: ${{ github.event.issue.title }}"
```

is not passing a variable — it pastes attacker-controlled text directly into your shell command. An
issue titled `"; <attacker-command> #` is concatenated in and executed. This one mechanism is the
most common real-world Actions vulnerability, and models routinely generate it. Treat every `${{ }}`
that contains data an outside contributor can influence as a code-injection sink.

## Execution workflow — follow in order for every workflow reviewed

**Step 1 — Map the triggers and trust level.** Read every `on:` trigger and classify privilege:
`push` / same-repo `pull_request` run with the contributor's own trust; a **fork** `pull_request`
gets a **read-only** token and **no secrets** (safe by design); `pull_request_target`,
`workflow_run`, `issue_comment`, `issues` run in the **base repo** with a **read/write token and full
secret access** yet can be **triggered by outsiders** — the dangerous ones. See
`references/triggers-and-privilege.md`.

**Step 2 — Hunt for script injection.** For every `run:` block, every `actions/github-script`
`script:`, and every custom-action input, list the `${{ }}` expressions and check whether any
resolve to attacker-controllable data (issue/PR title/body, `head.ref`/`head.label`, comment/review
bodies, commit messages, `github.head_ref`, any fork-settable `github.event.*`). See
`references/injection.md` for the full sink list and the `env:`-passthrough fix.

**Step 3 — Check privileged triggers don't execute untrusted code.** If a `pull_request_target` /
`workflow_run` workflow checks out PR/fork code (`ref: …head.sha`) **and runs it** (build, test,
`npm install` lifecycle scripts, etc.), that is RCE against a privileged token — flag **CRITICAL**.
Safe pattern: split into an unprivileged `pull_request` workflow that runs the untrusted code and a
privileged `workflow_run` workflow that only consumes its results.

**Step 4 — Audit `permissions:`.** No `permissions:` block → inherits the repo default (possibly
read/write to everything) → flag. Recommend top-level `permissions: {}` (deny-all) or
`contents: read`, then grant the minimum per job. Flag `write-all` and any unused broad `write`. See
`references/permissions-and-tokens.md`.

**Step 5 — Audit action references (supply chain).** Third-party actions (not `actions/*` /
`github/*`) MUST be pinned to a full 40-char commit SHA — tags and branches are mutable and a
compromised upstream can repoint them. Flag `@main`/`@master`/any branch as **HIGH**. Keep a
trailing `# vX.Y.Z` comment. This is Stratt's §7.3 pinning discipline applied to CI. See
`references/supply-chain.md`.

**Step 6 — Check secret and output handling.** No secrets echoed/logged; no `set -x`/`bash -x` in
steps touching secrets; secrets never passed to steps running untrusted code or to unpinned
third-party actions; untrusted data written to `$GITHUB_ENV`/`$GITHUB_OUTPUT` uses the
random-delimiter heredoc form; set `persist-credentials: false` on `actions/checkout` when the job
later runs untrusted code.

**Step 7 — Produce the report.** Use `references/report-format.md`: severity summary table first,
then findings grouped by issue type, each with file, exact offending YAML, plain-English risk, and a
concrete before/after fix. **Never auto-apply** — present for review.

## Severity guide (text labels — no emoji, charter §3.1)

| Severity | Meaning | Example |
|---|---|---|
| CRITICAL | Token/secret theft or RCE reachable by an outside contributor | `pull_request_target` checking out and running fork code; `${{ github.event.* }}` in a `run:` on a privileged trigger |
| HIGH | Exploitable supply-chain or scope problem | Third-party action on a mutable tag/branch; `write-all`; injection sink on `issue_comment` |
| MEDIUM | Risk under conditions or chaining | Missing `permissions:` block; secret reachable by a non-fork PR author |
| LOW | Hardening gap, low direct risk | First-party action not SHA-pinned; `persist-credentials` default on a non-privileged job |
| INFO | Observation, not a vulnerability | Version comment missing next to a pinned SHA |

## Output rules

- Always show the severity summary table first; group findings by issue type, not by file.
- Be exact — quote the offending line and give its location.
- Pair every CRITICAL/HIGH with a concrete corrected YAML snippet.
- Do **not** call a fork `pull_request` dangerous merely for running untrusted code — it has no
  secrets and a read-only token. Reserve CRITICAL for the privileged triggers.
- If the workflow is already hardened, say so and list what was checked.

## Reference files (load as needed)

- `references/triggers-and-privilege.md` — trust matrix per trigger; why `pull_request_target` /
  `workflow_run` are privileged; the two-workflow safe pattern.
- `references/injection.md` — attacker-controllable `${{ }}` contexts and the `env:`-variable safe
  pattern for each sink (`run`, `github-script`, action inputs).
- `references/permissions-and-tokens.md` — `GITHUB_TOKEN` scopes, least-privilege recipes, OIDC /
  keyless (cosign) auth instead of long-lived secrets.
- `references/supply-chain.md` — SHA-pinning, Dependabot for `github-actions`, artifact/cache
  poisoning, self-hosted-runner exposure, `checkout` credential persistence.
- `references/report-format.md` — output template: summary table, finding cards, before/after blocks.
