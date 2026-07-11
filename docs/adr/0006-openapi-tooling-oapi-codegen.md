# ADR 0006 — OpenAPI tooling: spec-first with oapi-codegen

- **Status:** Accepted
- **Date:** 2026-07-11
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.4, §1.5, §1.7, §3
- **Narrows:** charter §3's "API is OpenAPI-first (huma / oapi-codegen)" to one tool.

## Context

The charter sanctions two OpenAPI-first options without choosing: huma (code-first — Go
handlers generate the spec) and oapi-codegen (spec-first — hand-authored YAML generates
server stubs). dependency-scout evaluated both (2026-07-11).

oapi-codegen: Apache-2.0; deliberately migrated out of the defunct Deepmap org into a
community org in 2024 — first-party rug-pull-resistance behavior, the exact §7.2 signal;
two balanced lead maintainers; pure codegen composing with plain `net/http`, no bundled
framework. Cautions: OpenAPI 3.1 not yet natively supported (issue #373 open); one
historical ~9.5-month release stall (cadence since normalized to ~monthly); the coupled
`oapi-codegen/runtime` module must be version-matched.

huma: technically ahead on OpenAPI 3.1/JSON Schema 2020-12, healthy cadence, but ~96%
single-author commit concentration (the §1.7 fossilization pattern), a sprawling
one-module framework requiring every popular router in its `go.mod`, and a code-first
model that derives the schema from Go struct reflection — the inverse of Stratt's
"schemas are pinned data, never language classes" posture (§1.5).

## Decision

1. **The control-plane API is spec-first:** a hand-authored, git-tracked OpenAPI document
   is the source of truth; **oapi-codegen v2.7.2** (+ `oapi-codegen/runtime` v1.2.0)
   generates types and `net/http` server stubs from it. Handlers conform to the spec,
   compiler-enforced — never the reverse.
2. **The spec is authored in the OpenAPI 3.0.3 dialect** (`nullable: true` style) until
   oapi-codegen's 3.1 support lands (track upstream #373), then migrated deliberately.
3. Generated code is committed and regeneration diffs are reviewed in CI.

## Charter alignment

- **§1.5 sovereign contracts / schemas-are-data:** the API contract is a pinned document,
  consistent with how Facet/Contract schemas work everywhere else in the platform.
- **§1.4 boring spine:** single-purpose codegen + stdlib `net/http`, no framework adoption.
- **§1.7 evergreen / §7.2 governance:** the community-org migration history and balanced
  maintainer base are the deciding evidence.
- No Founding Discipline or non-goal is touched; this narrows an existing §3 commitment.

## Consequences

- **Positive:** one reviewable spec drives server, docs, and (later) the `/api/v2` façade
  and MCP surface; curl-ability and generated clients come free; no runtime framework
  lock-in.
- **Negative / trade-offs:** spec-first means authoring YAML before handlers (accepted —
  it is the point); OpenAPI 3.1 gap means the API envelope dialect temporarily differs
  from the 2020-12 JSON Schema used by Facet/Contract schemas.
- **Follow-ups:** evergreen gate watches both modules as a coupled pair; alert if the
  latest oapi-codegen tag is >120 days old; migrate the spec to 3.1 when #373 closes;
  regeneration check in CI (`git diff --exit-code` after codegen).

## Alternatives considered

- **huma** — rejected: single-maintainer concentration, framework-shaped dependency
  surface, and code-first schema derivation against the §1.5 posture, despite better
  OpenAPI 3.1 support today.
- **Hand-rolled handlers + hand-maintained spec** — rejected: the two drift; charter §3
  explicitly warns against handlers drifting from the published schema.
