// Package awxapi is a read-only client for an AWX 24.6.1 /api/v2 REST surface.
//
// TWO LIVE ROLES, and neither is the "one-shot migration importer" this doc used to name —
// ADR-0086 D1 retired that verb and ADR-0089 D5 deleted it:
//
//  1. ReadJobTemplate (adopt_read.go) — the ADR-0086 model-(b) TARGETED deep-read for one
//     already-observed object, invoked in-pod by the adopt/materialize path. This is the
//     production caller.
//  2. Enumerate (enumerate.go) — the BULK read. No production caller today, deliberately, and
//     retained per ADR-0089 D5 for a bounded future bulk-adopt. It also builds the golden CaC
//     contract fixture — see its own comment before deciding it is dead code.
//
// This is NOT a registered Connector/Syncer: it never projects Entities and
// must never be wired into the Syncer registry. It reads an AWX instance and
// hands a plain in-memory Snapshot to the sibling materialize transform,
// which emits Git-declared desired state (Views / Workflows / CredentialRefs) —
// the migration target is desired state, not the projection graph (§1.2).
//
// The import target is frozen at AWX 24.6.1 forever — "the friendliest
// migration in software" (charter §5.6).
//
// AWX API nouns (inventory, job_template, credential, playbook) appear here as
// the vendor's own REST rendering — legal as JSON tags, endpoint strings, and
// internal decode-struct field names (the latitude msgraph.device takes, §2).
// They must never surface as Stratt core-model identifiers in the emitted
// bundle; that boundary is the transform's responsibility.
package awxapi
