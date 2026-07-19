# ADR 0078 — Rolling-upgrade schema discipline: expand/contract + a pre-upgrade migration Job

- **Status:** Accepted
- **Date:** 2026-07-19
- **Deciders:** steward (dstout)
- **Charter sections:** §1.4 (boring spine; lean on the substrate), §1.7 (evergreen/upgrade-friendliness — "never become the monolith fossil AWX did"), §1.8 (never hide failure), §3 (K8s-native operator posture)
- **Implements:** the enterprise-readiness crack fix **UPG-1**
- **Relates to:** ADR-0004 (goose migrations), ADR-0040 (HA/DR)

## Context

Migrations ran **in-process at every pod boot** behind a Postgres advisory lock (`graph.Connect` → `Migrate`). The lock serialises concurrent `Up()` calls so N replicas don't corrupt each other — but it does **not** make a rolling upgrade safe. During a `kubectl`/Helm rolling update, new-version pods and old-version pods run **simultaneously** against **one** database. A new pod that applies a **breaking** migration (drop a column, narrow a type, `SET NOT NULL`) breaks the **still-serving old replicas** — an availability incident, mid-deploy, exactly when §1.7's "upgrade-friendliness is a first-class criterion" says we must be safest. There was no discipline preventing it and no controlled place to run migrations.

## Decision

**Two parts: a controlled *when*, and a compatibility *rule*.**

**1. A pre-upgrade migration Job owns schema changes (the *when*).** Lean on Helm's native hook lifecycle rather than in-app coordination (§1.4): an opt-in `pre-install,pre-upgrade` **hook Job** runs the control-plane binary in a new `STRATT_MIGRATE_ONLY` mode (apply migrations, exit 0) **once**, before the serving pods roll. The serving Deployment then boots with `STRATT_SKIP_MIGRATE`, so N replicas never race `Up()`. New seams: `graph.MigrateURL` (one-shot) and `graph.ConnectNoMigrate` (serving replicas). Default behaviour is unchanged (boot-time migrate) so single-replica/dev is untouched; the Job is for HA rollouts.

**2. The expand/contract rule (the *compatibility*).** The hook applies the new schema **while the old replicas are still serving**, so a migration must be compatible with the *previous* release's code:
- **Expand** (this release): additive-only — new tables/columns/indexes, widened constraints. Old and new code both work.
- **Contract** (a *later* release, after every replica is new): the destructive step — drop the now-unused column, add the `NOT NULL`, narrow the type.

`task migrate:lint` (wired into `task ci`) **fails the build** when a migration's `Up` section contains a destructive statement (`DROP TABLE/COLUMN/CONSTRAINT`, `RENAME`, `ALTER COLUMN … TYPE`, `SET NOT NULL`) unless the file carries an explicit `-- expand/contract-ok: <reason>` marker — the same loud, reviewed opt-out pattern the rest of the platform uses. Three historical migrations (constraint *widenings* + one pre-discipline change) are grandfathered with the marker.

## Charter alignment

Upholds §1.7 (a rolling upgrade is now safe by construction — the single loudest way a platform "becomes the fossil" is unsafe migrations; this is the discipline against it), §1.4 (Helm hooks are the boring, K8s-native orchestrator — no bespoke migration coordinator), §1.8 (`migrate:lint` makes the unsafe case a build failure with a clear message, not a silent prod incident), and §3 (operator-native lifecycle). No new dependency (goose already ships); no new Named Kind.

## Consequences

- **Positive:** HA rollouts are safe; migrations run once in a controlled step; the destructive-change footgun is a CI failure, not a 2 a.m. incident; the discipline is documented and enforced.
- **Negative / trade-offs:** a genuinely destructive change now spans **two releases** (expand, then contract) — more deliberate, but that is the price of zero-downtime upgrades and is the industry-standard trade. The lint is heuristic (regex over `Up` SQL) — it can't prove semantic compatibility, only catch the obvious destructive verbs; the `expand/contract-ok` marker is a human judgement, logged.
- **Follow-ups:** a `down`-then-`up` idempotency test behind CI Postgres (crack MIG-1); optionally make the hook the default once the Job path has soaked.

## Alternatives considered

- **Keep boot-time migration only** — rejected: it is the status quo that makes rolling upgrades unsafe; the advisory lock prevents corruption, not skew.
- **A separate migration tool/operator** — rejected (§1.4): goose + a Helm hook is the boring path; a bespoke operator is more moving parts for no gain.
- **Block all destructive migrations outright** — rejected: contraction is legitimate in a later release; the rule is *sequence* them across releases, not forbid them. The marker encodes the acknowledgement.
