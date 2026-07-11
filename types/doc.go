// Package types holds the shared wire/domain types for Stratt's Named Kinds
// (charter §2, frozen at v1.0). It is consumed by the control plane and, from
// Phase 3, by the stratt-agent pull agent — one language, one set of types
// (.claude/rules/backend-go.md).
//
// Two boundaries these types must never cross:
//
//   - Contracts and Facet schemas are DATA — pinned, hash-verified JSON Schema
//     documents (charter §1.5, §2.2). Nothing in this package models a Contract;
//     a Go struct here is an internal convenience, never the contract of record.
//   - The graph is a projection (charter §1.2). These types describe projected
//     state and its Provenance; desired state lives in Git, not here.
package types
