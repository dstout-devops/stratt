package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dstout-devops/stratt/types"
)

// identityCorrelator is the WriterRef for the platform-computed identity edges +
// Findings (ADR-0079 slice 4a). Run-provenance semantics: these are DERIVED from
// already-projected facets, not observed from an external Source.
const identityCorrelator = "identity-correlator"

// CorrelateIdentities links credential Entities to the subjects they attest — the
// `identifies` edge (ADR-0079) that ends the credential island — and raises a
// Finding when a credential attests a DEACTIVATED identity: the "a leaver still
// holds a valid credential" case that no island model can see, because it spans
// two sources (the PKI's cert and the IdP's user). A cert `identifies` a subject
// when its attested subjectName matches a subject's name / email / principal.
//
// Platform-computed (run provenance): a derived correlation over projected
// identity.credential + identity.subject facets, never an external observation.
// Idempotent (re-runs converge on the same edges + open Findings). Best-effort at
// the caller; a failure keeps the previous correlation (§1.8, logged).
func (s *Store) CorrelateIdentities(ctx context.Context) error {
	// Index every subject by the keys a credential's subjectName might carry.
	type subject struct{ id, status string }
	index := map[string]subject{}
	subjRows, err := s.pool.Query(ctx, `
		SELECT f.entity_id, f.value
		FROM graph.facet f
		JOIN graph.entity e ON e.id = f.entity_id
		WHERE e.kind IN ('user','group') AND f.namespace = 'identity.subject' AND e.deleted_at IS NULL`)
	if err != nil {
		return fmt.Errorf("identity-correlation: read subjects: %w", err)
	}
	for subjRows.Next() {
		var id string
		var raw []byte
		if err := subjRows.Scan(&id, &raw); err != nil {
			subjRows.Close()
			return fmt.Errorf("identity-correlation: scan subject: %w", err)
		}
		var v struct{ Name, Email, PrincipalID, Status string }
		if err := json.Unmarshal(raw, &v); err != nil {
			continue
		}
		for _, k := range []string{v.Name, v.Email, v.PrincipalID} {
			if k != "" {
				index[strings.ToLower(k)] = subject{id: id, status: v.Status}
			}
		}
	}
	subjRows.Close()
	if err := subjRows.Err(); err != nil {
		return fmt.Errorf("identity-correlation: subjects: %w", err)
	}

	// Buffer the credentials before writing (the write acquires its own conn).
	type credential struct {
		id, subjectName, serial string
	}
	var creds []credential
	certRows, err := s.pool.Query(ctx, `
		SELECT f.entity_id, f.value
		FROM graph.facet f
		JOIN graph.entity e ON e.id = f.entity_id
		WHERE e.kind = 'cert' AND f.namespace = 'identity.credential' AND e.deleted_at IS NULL`)
	if err != nil {
		return fmt.Errorf("identity-correlation: read credentials: %w", err)
	}
	for certRows.Next() {
		var id string
		var raw []byte
		if err := certRows.Scan(&id, &raw); err != nil {
			certRows.Close()
			return fmt.Errorf("identity-correlation: scan credential: %w", err)
		}
		var v struct{ SubjectName, SerialNumber string }
		if err := json.Unmarshal(raw, &v); err != nil {
			continue
		}
		creds = append(creds, credential{id: id, subjectName: v.SubjectName, serial: v.SerialNumber})
	}
	certRows.Close()
	if err := certRows.Err(); err != nil {
		return fmt.Errorf("identity-correlation: credentials: %w", err)
	}

	proj := s.RunProjector()
	prov := types.Provenance{WriterKind: types.WriterRun, WriterRef: identityCorrelator}
	for _, c := range creds {
		if c.subjectName == "" {
			continue
		}
		sj, ok := index[strings.ToLower(c.subjectName)]
		if !ok {
			continue // the credential attests a subject not projected as an identity — not a leaver signal
		}
		if err := proj.UpsertRelation(ctx, prov, "identifies", c.id, sj.id); err != nil {
			return fmt.Errorf("identity-correlation: identifies %s→%s: %w", c.id, sj.id, err)
		}
		if sj.status == "disabled" {
			detail, _ := json.Marshal(map[string]any{
				"credential":  c.id,
				"serial":      c.serial,
				"subject":     sj.id,
				"subjectName": c.subjectName,
				"reason":      "a valid credential attests a DEACTIVATED identity — a leaver still holds it; revoke at the PKI SoR (INV-2)",
			})
			// The remediation flows to the SoR (revoke the cert), never a graph edit (INV-2).
			if err := s.WriteGovernanceFinding(ctx, "identity/leaver-credential", c.id, "warning", "identity/leaver-credential", detail); err != nil {
				return fmt.Errorf("identity-correlation: leaver finding %s: %w", c.id, err)
			}
		}
	}
	return nil
}
