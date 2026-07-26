# Parity audits

A **parity audit** answers one question about one external system: _what does it do that we don't?_ —
object by object, feature by feature, with the implementation status of each traceable to a file or an
ADR.

These are not roadmaps and not marketing. A parity audit is allowed to be unflattering; its value is
entirely in being honest about what is unexamined, and it earns nothing by scoring itself well. The
[roadmap](../roadmap.md) tracks what we are building; [enterprise-readiness](../enterprise-readiness.md)
tracks what would embarrass us in front of a customer; **parity tracks what a competitor's user would
miss on the day they switch.**

| Audit                                          | Subject                                                    | Level                  | Last pass  |
| ---------------------------------------------- | ---------------------------------------------------------- | ---------------------- | ---------- |
| **[aap-2.7-platform.md](aap-2.7-platform.md)** | Red Hat Ansible Automation Platform 2.7 (all 6 components) | component / capability | 2026-07-19 |
| **[awx-object-model.md](awx-object-model.md)** | The AWX 24.6.1 `/api/v2` object model                      | object / field         | 2026-07-26 |
| **[ansible-tool.md](ansible-tool.md)**         | Ansible itself — content-root shape + execution surface    | feature / flag         | 2026-07-26 |

## Conventions

**Every row carries evidence.** A 🟢 with no file or ADR link is not a finding, it is a claim. If a row
cannot be evidenced it is 🔴 or 🟠, not 🟢 — "we probably do that somewhere" is exactly the state these
documents exist to eliminate.

**Status legend** (shared across all parity docs):

| Symbol | Meaning                                                                                         |
| ------ | ----------------------------------------------------------------------------------------------- |
| 🟢     | Shipped and evidenced in-repo                                                                   |
| 🟡     | Partial — the seam exists, the depth does not; the row says which half                          |
| 🟠     | **Unexamined** — nobody has looked; distinct from 🔴, which is a decision                       |
| 🔴     | Absent, and the absence is known                                                                |
| ⚪     | Deliberately not ours — a charter non-goal, or answered differently by design; the row says why |

🟠 is the symbol that makes these documents worth writing. A gap you have decided not to close is
managed; a gap nobody has looked at is unmanaged, and the two must never render identically.

**Stable IDs.** Every gap row gets an ID that outlives its row position, so an ADR, a commit message, or
a follow-up can name it. IDs are permanent: when a gap closes, the row stays with the ID struck through
and the closing ADR named. Never renumber, never reuse.

| Namespace | Owned by            | Example                                        |
| --------- | ------------------- | ---------------------------------------------- |
| `P#`      | aap-2.7-platform.md | `P5` (EE build factory) — predates this folder |
| `AWX-###` | awx-object-model.md | `AWX-014`                                      |
| `ANS-###` | ansible-tool.md     | `ANS-007`                                      |

The `P#` namespace is bare and unprefixed for history: ADR-0117 and ADR-0125 already cite `P2`/`P3`/`P5`
in prose, so renaming them would break references to buy consistency nobody needs.

**Provenance of the "what they have" column.** The Stratt column of every table is verified in-repo and
linked. The external-system column is not, and cannot be — it comes from vendor documentation, release
notes, and API surfaces, read at the date the audit says. Where a row's external claim would change the
verdict if wrong, the row says where it came from. Do not silently upgrade a documentation reading into a
verified fact.

**Refresh discipline.** A parity audit is a dated snapshot, not a live view. Re-run a pass when the
subject ships a major version, or when an ADR closes something one of these rows tracks — and update the
`Last pass` date above when you do, even if nothing changed, so a stale audit is visibly stale rather
than quietly wrong.

## Adding an audit

1. Pick a namespace prefix nobody owns and record it in the table above.
2. Lead with the scorecard, then per-area detail, then a prioritized gap list. Readers who take one
   thing away should take the scorecard.
3. State the evidence base and the date in the header — what you read, and what you ran.
4. Mark anything you did not examine 🟠 rather than omitting it. An omission reads as coverage.
