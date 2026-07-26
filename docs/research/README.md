# Research

**Studies that precede decisions.** A research note surveys how other systems solved a problem, works our
scenarios through the candidate models, and ends with **open questions and a proposed ADR queue** — never
with a decision. The decision lives in an ADR; this is the argument you have _before_ writing one, and the
thing a reviewer can disagree with without re-doing the reading.

The distinction from the folder next door: [`parity/`](../parity/README.md) audits **what we have against
what a specific product has**, row by row with evidence. Research asks **what shape a thing should be**,
usually before we have anything at all.

| Study                                              | Question it answers                                                                                            | Last pass  |
| -------------------------------------------------- | -------------------------------------------------------------------------------------------------------------- | ---------- |
| [multi-team-ownership.md](multi-team-ownership.md) | How do many teams publish and consume each other's automation, gated, without losing lights-off Cell handover? | 2026-07-26 |

## Conventions

**Cite the primary source, and say when you did not.** Every external claim carries a link. Vendor
documentation read on a date is a documentation reading, not a verified fact — say so, the way the parity
audits do.

**Work the real scenarios end to end.** A model that reads well and cannot express a scenario the business
actually has is a model that failed. Name the scenarios up front, then trace each one through.

**Separate what we already have from what we would need.** The most useful output of a study is usually
"we have four of these six pieces" — that reframes a build as a gap-fill, and it stops a design from
re-inventing a seam that already ships.

**End with questions, not answers.** A study that concludes is an ADR wearing the wrong hat. The last
section is the ADR queue it implies, in dependency order, with what each one must decide.

**Studies date, they do not rot.** Record the date and the sources; when the subject moves, add a pass
rather than silently editing history.

## Candidates that predate this folder

Two existing documents are research by nature and could move here when someone is touching them anyway —
left in place for now because moving them breaks inbound links for no reader's benefit:

- [`oss-connector-tool-landscape.md`](../oss-connector-tool-landscape.md) — a survey of Apache-2.0-compatible
  tools for the Connector/Actuator surface.
- [`ux/competitive-teardown.md`](../ux/competitive-teardown.md) — a competitive teardown.
