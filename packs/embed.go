// Package packs holds Stratt's in-tree content packs (ADR-0033): curated
// groupings of existing Named Kinds — a collector Trigger that projects Facets
// (charter §1.2) plus facet-observation Baselines that assert expected values.
// "Pack" is NOT a Named Kind (charter §2); this is an authoring/distribution
// grouping only. Like contracts/, this module is DATA — reviewable at the repo
// root, embedded and hash-pinned. The load/materialize logic lives in the
// control plane (core/internal/packs). OCI-cosign-signed distributable packs
// are the documented next layer (ADR-0033), not built here.
package packs

import "embed"

// FS carries every pack's content. Each top-level directory is one pack, with
// a manifest.yaml and CaC content under triggers/ and baselines/. Add a new
// pack by adding its directory to this embed list.
//
//go:embed cis
var FS embed.FS
