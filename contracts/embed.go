// Package contracts holds Stratt's pinned Contract and Facet-schema
// documents (charter §1.5, §2.2): JSON Schema as DATA — reviewable at the
// repo root, embedded into the control plane, hash-verified at registration.
// This module deliberately contains no logic; validation lives in the
// control plane (core/internal/contract) using a standard validator.
package contracts

import "embed"

// FS carries every schema document. Paths are the Contract names:
// actuators/<name>.input.schema.json, facets/<namespace>.schema.json,
// outputs/<name>.schema.json, intents/<kind>.schema.json,
// actions/<connector>/<op>.input|output.schema.json,
// policy/<name>.schema.json (the PDP request/decision Contract, ADR-0062).
//
//go:embed actuators/*.schema.json facets/*.schema.json outputs/*.schema.json intents/*.schema.json actions/*/*.schema.json policy/*.schema.json
var FS embed.FS
