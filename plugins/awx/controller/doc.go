// Package awx is a read-only client for an AWX 24.6.1 /api/v2 REST surface,
// used by the one-shot migration importer (charter §5.6 "AWX exodus"; Flow 6).
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
package controller
