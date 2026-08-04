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

## Audit discipline — added 2026-08-04, after six rows in one day were wrong

Six rows across [ansible-tool.md](ansible-tool.md) and [aap-2.7-platform.md](aap-2.7-platform.md)
were corrected on 2026-08-04. Every one had drifted the same way: **the code shipped and the row did
not move.** Two were recommended to the steward as the next work before anyone read the tree; one
cited a function (`injectionFor`) that is not the mechanism it described and lives in a different
component for a different purpose.

That failure mode is worse than a stale row looks. These documents exist to be QUOTED — by planning,
by ADR context sections, and by anyone deciding what to build next. A row claiming a capability is
missing is an invitation to build it twice, and the credential-injectors row nearly bought a new
configuration language to replace a declarative mechanism that already worked.

**So, before quoting any row here:**

1. **Read the code the row names.** If the row names no code, that is itself the finding.
2. **A row is evidence of the day it was written**, exactly like a demo's green. If a claim outlives
   the run or the reading that produced it, re-verify before repeating it.
3. **Correct in place, with the evidence** — struck through and dated, never quietly rewritten. The
   history of a wrong claim is how the next reader calibrates how much to trust the rest.
4. **Not everything is stale.** The same pass confirmed CLI query verbs (`plan`/`apply` only, no
   `get`/`describe`), cost accounting (calls and errors, no run-minutes), and the absent Organization
   container are all ACCURATE. Verification is the point, not scepticism.
