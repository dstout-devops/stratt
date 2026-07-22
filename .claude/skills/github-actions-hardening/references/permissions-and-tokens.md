# Permissions and Tokens

Every workflow run gets an automatic `GITHUB_TOKEN`. Its scope is the blast radius if a step is
compromised, so scope it to the minimum.

## The default is too broad

With no `permissions:` block, the workflow inherits the repository/organization default. On older or
permissive repos that default is **read/write to most scopes**. A single injected command or
malicious dependency then runs with the ability to push code, publish releases, or approve PRs.

## Least-privilege recipe

Set a restrictive default at the top level, then elevate per job only where needed.

```yaml
# Deny by default
permissions: {}

jobs:
  build:
    permissions:
      contents: read          # checkout only
    runs-on: ubuntu-latest
    steps: [...]

  comment:
    permissions:
      contents: read
      pull-requests: write    # this job posts a comment; nothing else
    runs-on: ubuntu-latest
    steps: [...]
```

Common scopes: `contents`, `pull-requests`, `issues`, `actions`, `packages`, `id-token`,
`deployments`, `checks`, `statuses`. Each is `read`, `write`, or `none`.

## Findings to flag

- No `permissions:` block anywhere → MEDIUM (inherits possibly-broad default).
- `permissions: write-all` → HIGH.
- A `write` scope the job's steps never use → HIGH (drop it).
- Top-level `write` that should live on one job → MEDIUM (move it down).

## OIDC / keyless instead of long-lived secrets

Storing static credentials as repo secrets means a leak is permanent until manually rotated. Prefer
OpenID Connect: the workflow requests a short-lived token the verifier trusts, scoped to that
repo/branch, expiring in minutes. `id-token: write` is the permission that enables it.

**For Stratt specifically (charter §7.3):** the highest-value OIDC use is **cosign keyless signing**
of images/Bundles and SLSA provenance — no long-lived signing key in secrets at all. Registry pushes
(ghcr) can likewise use the workflow's `packages: write` scope rather than a stored PAT. The generic
cloud pattern below is the same mechanism; Stratt is cloud-neutral, so treat any `aws-`/`azure-`
example as illustrative, not prescriptive.

```yaml
permissions:
  id-token: write     # request the OIDC token (also what cosign keyless uses)
  contents: read
  packages: write     # push images to ghcr without a stored PAT
jobs:
  sign:
    runs-on: ubuntu-latest
    steps:
      - uses: sigstore/cosign-installer@<sha>
      - run: cosign sign --yes "$IMAGE_DIGEST"   # keyless — identity from the OIDC token
        env:
          IMAGE_DIGEST: ${{ steps.build.outputs.digest }}
```

On the verifier side, scope trust to the specific repo and ideally a specific branch/environment so a
fork or another repo cannot assume the identity.

## Secret hygiene

- Reference secrets only in the jobs that need them.
- Never `echo` a secret or enable shell tracing (`set -x`) in a step that handles one.
- Don't pass secrets into third-party actions you haven't pinned and reviewed.
- Fork `pull_request` runs get no secrets — don't "fix" that by switching to `pull_request_target`
  (see `triggers-and-privilege.md`).
