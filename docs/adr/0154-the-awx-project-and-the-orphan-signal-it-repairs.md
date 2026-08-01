# ADR 0154 — The AWX Project, and the orphan signal it repairs

- **Status:** **Proposed** (2026-07-31, steward). Charter review by hand — this session's rules bar the
  subagent; §1.1/§1.2/§1.8/§2.5/§9 answered inline. **No new dependency.**
- **Date:** 2026-07-31
- **Deciders:** steward
- **Charter sections:** §1.1 (type the seams / own what you project), §1.2 (projections, never a
  second truth), §1.8 (never hide diagnosis), §2.5 (credentials brokered, never baked), §9 (no
  ontology creep)
- **Closes AWX-001**, booked by **ADR-0127 D4** and ranked Tier 1 by the object-model audit.
  **Repairs a soundness gap in ADR-0085's orphan Baseline.** Supersedes nothing.

## Context

The AWX object-model audit lists `projects` as the last `adopt-only` 🔴 in the projection column, and
ranks it alone in Tier 1:

> **AWX-001 · `ansible.project` + `scm_revision`.** Already booked by ADR-0127 D4 and unchanged by
> this audit — **it repairs ADR-0085's soundness**, and it deserves its own ADR.

ADR-0127 itself booked it in the same words: "`ansible.project` + `scm_revision` binding catalogue to
execution (**and with it, ADR-0085's soundness**)".

### The soundness gap, stated exactly

ADR-0085's orphan-template Baseline reads the presence of one cross-source edge:
`ansible.template --runs--> ansible.playbook`. The Controller half emits it; the content half owns
the target. The edge is SOFT — the host resolves it and drops it if the playbook is not projected —
and **that dropped edge IS the signal**: "your Controller runs content Stratt cannot see."

The problem is how the target is named. From `runsRel`:

```go
proj, pb := jt.SummaryFields.Project.Name, jt.Playbook
… ToValue: proj + "/" + pb
```

The AWX Project's **NAME**, concatenated with the playbook path, matched against the content half's
`<projectID>/<relpath>`, where `projectID` is the operator-set `STRATT_ANSIBLE_CONTENT_ID`. The
existing comment is honest about this: "the operator aligns STRATT_ANSIBLE_CONTENT_ID with the AAP
Project name".

So the edge rests on a **convention an operator types into an environment variable**, and when the
convention is broken the edge drops — which is byte-identical to the edge dropping because the
content genuinely is not projected. **One signal, two very different causes, and no way to tell them
apart.** ADR-0127's own consequences section already flags the adjacent symptom: in a Controller-only
install every template is an orphan and "it reads as noise".

That is a §1.8 failure of the kind this repo keeps finding: not a wrong answer, an _undiagnosable_
one.

### Prior art this must reconcile with (scanned by hand — subagents barred this session)

- **ADR-0085** — the relation-presence Baseline. Its mechanism is unchanged here; what changes is
  that the operator gets a second, exactly-joined fact to descend into when it fires.
- **ADR-0127 D1/D4** — two Sources under one plugin; the content half owns `ansible.playbook`, the
  Controller half may point at it but never own it. This ADR adds no cross-source ownership.
- **ADR-0128 D2** — the mirror carries an AWX credential's name and kind and **never material**. The
  same question arrives here in a different shape (see D3).
- **ADR-0131** — the poll-cost budget. This adds one COLLECTION read, not an N+1 tier.
- **ADR-0042** — per-source liveness; a new tombstone scheme retracts only this Source's entities.
- **§1.2** — AWX stays the system of record. Nothing here is written back.

## Decision

### D1 — `ansible.project` is projected, and the template→project edge joins on the ID

A new Kind carrying what an AWX Project actually is: `name`, `scmType`, `scmUrl`, `scmBranch`,
`scmRevision`, `status`, `lastUpdated`.

The template gains `ansible.template --uses-project--> ansible.project`, joined on
`summary_fields.project.ID` — **an identifier AWX issued**, not a name a human aligned. That edge
cannot silently mismatch, and it is what makes the orphan signal diagnosable:

| What an operator sees                      | What it means                                                                                                                                                                                                                         |
| ------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `uses-project` present, `runs` present     | fully joined; nothing to investigate                                                                                                                                                                                                  |
| `uses-project` present, `runs` **dropped** | the Controller's project is visible — its `scmUrl` and `scmRevision` are right there — so the missing half is the CONTENT root: either not projected at all, or projected under a `projectID` that does not match this project's name |
| `uses-project` **absent**                  | the template runs a manual project or AWX returned none — a different diagnosis entirely                                                                                                                                              |

Before this, the middle row and the "content genuinely absent" case were the same observation.

**The `runs` edge is NOT re-keyed.** It still joins on `<project.name>/<playbook>`, because the
content half genuinely identifies playbooks by a project id and a relative path and has no knowledge
of AWX ids — inventing a translation would be Stratt asserting a correspondence neither system
states (§1.2). The name join is the correct mechanism; what it lacked was a companion fact, and that
is what D1 supplies.

### D2 — `scmRevision` is the fact that binds the catalogue to execution

`scm_revision` is the commit AWX last synced. It is the single most useful field on the object,
because it is the only thing in the mirror that says **which bytes the Controller is actually
running**. A template's `playbook` names a file; the revision says which version of it.

It is projected as an OBSERVATION and nothing more. In particular this ADR does **not** compare it
to anything: the content half reads a filesystem through `fs.FS` and projects no git revision, so
there is no second value to diff. Claiming a drift check we cannot compute would be exactly the
plausible-wrong-answer this repo keeps refusing. When the content half learns to observe its own
checkout, the comparison becomes possible and is a follow-up — booked, not implied.

### D3 — an SCM URL is redacted at its userinfo, not dropped

`scm_url` is the fifth place in this Connector where §2.5 has to be answered, and it has its own
shape: AWX stores repository credentials as a separate object, but a real estate routinely contains

```
https://svc-account:ghp_REALTOKENHERE@github.com/acme/infra.git
```

because embedding a PAT in the clone URL works and nobody stopped them.

Dropping `scmUrl` entirely would lose the fact an operator most needs — _which repository_ — to
protect against a case that is a minority of URLs. Projecting it verbatim would import live tokens
into the graph. So the URL is projected with its **userinfo removed**:
`https://github.com/acme/infra.git`, with a `scmUrlRedacted` boolean saying that something was taken
out.

The boolean matters as much as the redaction. Silently stripping would leave an operator unable to
tell a clean URL from a scrubbed one — and "this repository is being cloned with an embedded
credential" is itself a finding worth surfacing (§1.8). Non-URL forms that cannot be parsed (an
`scm_type: git` local path, an `ssh://` variant with an odd shape) are projected verbatim only when
they contain no `@` before the host; otherwise the whole value is withheld and the flag is set,
because a value we cannot parse is a value we cannot prove is safe.

### D4 — one collection read, and the budget says so

`/projects/` is a COLLECTION read: O(1) per poll, not O(objects). The poll-cost literal moves
10 → 11 **deliberately**, which is what ADR-0131 D3's constant exists for.

Project **sync jobs** (`/project_updates/`) are NOT read. That is run history, which §3 forbids
mirroring, and `status` + `lastUpdated` on the project already carry the current state — the same
current-not-history line ADR-0128 D3 drew for templates.

## Consequences

**The orphan Baseline stops being ambiguous** without its logic changing. It still reads relation
presence; the operator now has an exactly-joined project entity to descend into when it fires, which
is the difference between "something is wrong" and "this is what is wrong".

**A Controller-only install reads better.** ADR-0127 flagged that every template is an orphan there
and "reads as noise". It still fires — correctly, the content IS unseen — but the projected project
now shows the scm_url and revision of the content in question, so the noise carries an address.

**One more namespace to own** (`ansible.project`), advertised in the manifest, granted in strattd,
and tombstoned per-source. The count checks in `TestHalvesOwnDisjointNamespaces` move with it, which
is where that class of omission gets caught — it caught one two commits ago.

**Not closed by this:** the revision COMPARISON (D2), which needs the content half to observe its own
checkout; and `scm_refspec` / project credentials, which are adopt-path fidelity rather than mirror
structure.
