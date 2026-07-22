# Supply Chain

A workflow runs other people's code every time it `uses:` an action. Those actions execute with your
token and (on privileged triggers) your secrets, so their integrity is your integrity. This is
Stratt's §7.3 pinning/provenance discipline applied to CI itself.

## Pin third-party actions to a commit SHA

Tags (`@v4`) and branches (`@main`) are **mutable** — the upstream owner (or anyone who compromises
them) can repoint them to new code without you changing a line. A full 40-character commit SHA is
immutable.

```yaml
# Mutable — the tag can be moved to malicious code
- uses: some-org/some-action@v3

# Pinned — this exact tree, forever
- uses: some-org/some-action@3f1e0a9c8b7d6e5f4a3b2c1d0e9f8a7b6c5d4e3f # v3.2.1
```

Rules:

- Third-party actions (anything not `actions/*` or `github/*`) → **MUST** be SHA-pinned. Flag tags
  and branches as HIGH.
- `@main` / `@master` → HIGH regardless of publisher; that is unversioned "latest".
- First-party `actions/*` → SHA-pinning is the hardened recommendation (LOW if only tag-pinned).
- Keep a trailing `# vX.Y.Z` comment so humans and Dependabot can read the intended version.

Not theoretical: real incidents have repointed popular actions' tags to code that exfiltrated secrets
from every workflow referencing the mutable tag.

## Let Dependabot update the pins

SHA pins go stale. Enable Dependabot for the `github-actions` ecosystem so updates arrive as
reviewable PRs (it reads the `# vX.Y.Z` comment and bumps the SHA). This is the CI-side counterpart
of Stratt's evergreen upgrade train (§1.7).

```yaml
# .github/dependabot.yml
version: 2
updates:
  - package-ecosystem: github-actions
    directory: /
    schedule:
      interval: weekly
```

## Artifact and cache poisoning

- An artifact uploaded by an untrusted `pull_request` build is **untrusted data**. A privileged
  `workflow_run` may download it, but must treat it as data only — never execute it, and validate
  paths when extracting (a crafted artifact can contain `../` path-traversal entries).
- Caches are keyed and can be populated by less-privileged runs; do not trust cached build outputs to
  be untampered in a privileged context.

## Self-hosted runners on public repos

GitHub-hosted runners are ephemeral — a fresh VM per job, destroyed after. **Self-hosted runners
persist**, so untrusted fork PR code running on one can leave behind tools/backdoors for the next
job, read other repositories' checkouts or credentials on the same machine, or pivot into your
network. Never use self-hosted runners for workflows public forks can trigger; if you must, use
ephemeral single-use runners and never expose secrets to fork-triggered jobs.

## `checkout` credential persistence

`actions/checkout` writes the token into `.git/config` by default so later `git` steps can push. If
the job subsequently runs untrusted code, that code can read the token. Set
`persist-credentials: false` when you don't need to push — especially before building/testing
untrusted code.
