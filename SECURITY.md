# Security Policy

## Reporting a vulnerability

**Please do not open a public issue for a security problem.**

Report privately through GitHub's [private vulnerability
reporting](https://github.com/dstout-devops/stratt/security/advisories/new) on this repository. If
that is unavailable to you, email **dstout.devops@gmail.com** with `SECURITY` in the subject.

Useful things to include, in rough order of usefulness: what an attacker gains, the smallest
reproduction you have, the version or commit, and whether it is already public somewhere.

## What to expect

| Stage                     | Target                                                                          |
| ------------------------- | ------------------------------------------------------------------------------- |
| Acknowledgement           | 3 working days                                                                  |
| Initial assessment        | 10 working days — severity, whether it reproduces, and whether a fix is planned |
| Fix or documented refusal | Tracked publicly once a fix ships, or on the disclosure date below              |
| Disclosure                | 90 days from acknowledgement, or when a fix is released — whichever is sooner   |

This project has no paid support tier and no embargo list. If the 90 days lapse without a fix, you
are free to disclose; we would rather be told about the delay than have you sit on it.

**Credit is offered by default.** Tell us the name or handle you want, or that you want none.

## Scope

**In scope** — anything that runs as part of a Stratt deployment:

- the control plane (`core/`), the pull agent, and the audit forwarder;
- the plugin port and the plugins in `plugins/`;
- the Helm chart in `deploy/charts/stratt` as it ships;
- the published container images and their signatures/attestations
  ([ADR-0165](docs/adr/0165-there-has-never-been-a-release-to-sign.md)).

**Out of scope**, and stated so nobody wastes a weekend:

- the dev harness and demo estates (`demos/`, `deploy/dev/`, `values-*dev*.yaml`). These deliberately
  use floating tags, an in-cluster root token, a trusted `X-Stratt-Principal` header and other
  postures that are unsafe by design and labelled as such in the files themselves. A finding of
  "the dev floor is insecure" describes an intended property.
- findings that require an attacker to already hold cluster-admin, the database, or a Principal's
  credentials — unless the finding is that they should not have been able to obtain them.
- vulnerabilities in Postgres, NATS, Temporal, OpenBao, Kubernetes or other substrate. Report those
  upstream; tell us if Stratt's *use* of them is what makes you exploitable.

## What this project promises about its own supply chain

Charter §7.3 commits to signed releases, SBOM and SLSA provenance. The honest current state
([ADR-0165](docs/adr/0165-there-has-never-been-a-release-to-sign.md)):

- the release pipeline exists, signs **keyless** (Sigstore — identity-bound to the release workflow,
  no long-lived key), attaches an SPDX SBOM and SLSA provenance, and **verifies its own output**;
- **nothing is published yet.** No image has been released, so there is no artifact to verify. When
  one exists, `task supply:verify IMAGE=<ref@sha256:…>` runs exactly what CI runs.

If you find that a published artifact does not verify, that is a security report — please send it.

## Security-relevant design, if you are looking for somewhere to start

- Credentials are brokered, never held by the control plane
  ([ADR-0052](docs/adr/0052-secretbroker-port.md)) — the invariant is that core never holds material,
  even transiently.
- Every capability is authorized through one model
  ([ADR-0009](docs/adr/0009-identity-authz-credential-brokering.md)/[ADR-0028](docs/adr/0028-view-scoped-execution-authz.md)),
  and execution is scoped by View.
- Bundles pulled by edge agents are cosign-verified in-process before execution
  ([ADR-0032](docs/adr/0032-sites-remote-execution-loci.md)).
- Inbound event sources authenticate by token or by signature verified against a key the control
  plane cannot read ([ADR-0164](docs/adr/0164-a-source-signs-and-the-core-does-not-hold-the-key.md)).
