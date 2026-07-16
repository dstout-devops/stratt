package graph

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/dstout-devops/stratt/types"
)

// Fenced cross-Cell Source re-home (ADR-0044 slice 7). The unit of re-home is the
// Source, not the Entity (charter-guardian must-fix 1/2): an Entity is a
// projection of a Source (§1.2), so the destination Cell RE-PROJECTS the
// Entities natively (rebuildable) rather than receiving shipped rows, and the
// source Cell tombstones its now-unobserved copies. The four store methods below
// are the phases the RehomeSourceWorkflow drives; the DB seal fence
// (enforce_write_path, migration 00031) is what makes the seal a true
// single-writer constraint rather than mere protocol.

// WritePathRehome is the mover's write path: it may tombstone Entities on the
// source Cell (setting deleted_at) even while their Source is sealed — the seal
// fence rejects only the 'normalizer' projection path, never the mover itself.
const WritePathRehome WritePath = "rehome"

// SealSourceForRehome fences a Source for a cross-Cell move (phase 1): it stamps
// rehoming_to = destCell and bumps home_epoch, so enforce_write_path immediately
// rejects this Cell's Normalizer projections of the Source — the single-writer
// fence, a DB constraint. It returns the sealed Source snapshot (CredentialRef
// NAME only, never material — §2.5) for the destination's adopt. Idempotent: a
// re-seal to the same dest just re-bumps the epoch. A Source already sealed to a
// DIFFERENT dest is a conflict (never two destinations).
func (s *Store) SealSourceForRehome(ctx context.Context, name, destCell string) (types.Source, error) {
	var src types.Source
	var cred, rehoming *string
	err := s.pool.QueryRow(ctx, `
		UPDATE graph.source
		SET rehoming_to = $2, home_epoch = home_epoch + 1
		WHERE name = $1 AND (rehoming_to IS NULL OR rehoming_to = $2)
		RETURNING id, kind, name, endpoint, credential_ref, cell, rehoming_to, home_epoch`,
		name, destCell,
	).Scan(&src.ID, &src.Kind, &src.Name, &src.Endpoint, &cred, &src.Cell, &rehoming, &src.HomeEpoch)
	if errors.Is(err, pgx.ErrNoRows) {
		// Either the Source is unknown, or it is already sealed to a DIFFERENT
		// destination — disambiguate so the caller sees which (§1.8).
		if cur, gerr := s.GetSource(ctx, name); gerr == nil && cur.RehomingTo != "" && cur.RehomingTo != destCell {
			return src, fmt.Errorf("graph: source %s is already sealed for re-home to cell %s", name, cur.RehomingTo)
		}
		return src, fmt.Errorf("%w: source %s", ErrNotFound, name)
	}
	if err != nil {
		return src, fmt.Errorf("graph: seal source for rehome: %w", err)
	}
	if cred != nil {
		src.CredentialRef = *cred
	}
	if rehoming != nil {
		src.RehomingTo = *rehoming
	}
	return src, nil
}

// AdoptSource claims a re-homed Source in THIS (destination) Cell (phase 2): it
// inserts the Source homed here, settled (rehoming_to NULL), at the carried
// epoch. The destination's already-deployed Connector then re-projects the
// Source's Entities natively (prov_cell = this Cell). Idempotent + epoch-fenced:
// a replayed adopt at a stale epoch is a no-op (a Temporal activity retry, or a
// late duplicate, cannot regress the home). Adopt is the point of no return
// (must-fix 4): once it commits, the workflow is roll-forward-only.
func (s *Store) AdoptSource(ctx context.Context, src types.Source, epoch int64) error {
	cell := s.projCell()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO graph.source (kind, name, endpoint, credential_ref, cell, rehoming_to, home_epoch)
		VALUES ($1, $2, $3, nullif($4, ''), $5, NULL, $6)
		ON CONFLICT (name) DO UPDATE
		SET kind = excluded.kind, endpoint = excluded.endpoint,
		    credential_ref = excluded.credential_ref, cell = excluded.cell,
		    rehoming_to = NULL, home_epoch = excluded.home_epoch
		WHERE graph.source.home_epoch < excluded.home_epoch`,
		src.Kind, src.Name, src.Endpoint, src.CredentialRef, cell, epoch,
	)
	if err != nil {
		return fmt.Errorf("graph: adopt source: %w", err)
	}
	return nil
}

// CompleteRehome retires a re-homed Source on its old (source) Cell after the
// destination has adopted it (phase 3). It tombstones the Source's now-unobserved
// Entities (deleted_at) so this Cell stops serving them, resolves their Findings
// with resolved_reason 'entity-rehomed', and removes the sealed Source row. An
// Entity co-observed by another still-local Source is NOT tombstoned — liveness
// is a union over Sources (ADR-0042). Runs on the 'rehome' write path (exempt
// from the seal fence). Returns the number of Entities tombstoned.
func (s *Store) CompleteRehome(ctx context.Context, name string) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("graph: begin complete rehome: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT set_config('stratt.write_path', $1, true)`, string(WritePathRehome)); err != nil {
		return 0, fmt.Errorf("graph: declare rehome write path: %w", err)
	}

	var sourceID string
	var rehoming *string
	err = tx.QueryRow(ctx, `SELECT id, rehoming_to FROM graph.source WHERE name = $1`, name).Scan(&sourceID, &rehoming)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("%w: source %s", ErrNotFound, name)
	}
	if err != nil {
		return 0, fmt.Errorf("graph: load source for complete: %w", err)
	}
	if rehoming == nil {
		// Not sealed — completing a move that was never sealed (or already
		// completed) is a no-op, not a silent success masking a bug.
		return 0, fmt.Errorf("graph: source %s is not sealed for re-home", name)
	}

	// Retract this Source's entire presence set, then tombstone the Entities
	// whose LAST presence row is now gone (mirror of TombstoneAbsent, but the
	// whole Source vanishes rather than a delta). Two statements: a data-modifying
	// CTE's DELETE is invisible to a same-statement NOT EXISTS under snapshot rules.
	rows, err := tx.Query(ctx, `
		DELETE FROM graph.entity_presence WHERE source_id = $1::uuid RETURNING entity_id`, sourceID)
	if err != nil {
		return 0, fmt.Errorf("graph: retract rehomed presence: %w", err)
	}
	retracted, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, fmt.Errorf("graph: collect retracted: %w", err)
	}

	var tombstoned int64
	if len(retracted) > 0 {
		tag, err := tx.Exec(ctx, `
			UPDATE graph.entity e
			SET deleted_at = now()
			WHERE e.id = ANY($1::uuid[])
			  AND e.deleted_at IS NULL
			  AND NOT EXISTS (SELECT 1 FROM graph.entity_presence p2 WHERE p2.entity_id = e.id)`,
			retracted)
		if err != nil {
			return 0, fmt.Errorf("graph: tombstone rehomed entities: %w", err)
		}
		tombstoned = tag.RowsAffected()

		// Resolve the tombstoned Entities' live Findings with the distinct
		// 'entity-rehomed' reason (must-fix 3) so descent shows the Entity moved
		// Cells, it did not vanish — and A's Findings never linger open forever.
		if _, err := tx.Exec(ctx, `
			UPDATE graph.finding f
			SET status = 'resolved', resolved_at = now(), consecutive_drifted = 0,
			    resolved_reason = $2
			FROM graph.entity e
			WHERE f.entity_id = e.id::text
			  AND e.deleted_at IS NOT NULL
			  AND e.id = ANY($1::uuid[])
			  AND f.status <> 'resolved'`,
			retracted, types.ResolvedEntityRehomed); err != nil {
			return 0, fmt.Errorf("graph: resolve rehomed findings: %w", err)
		}
	}

	// Remove the sealed Source row (source_sync cascades). The Entities are
	// tombstoned (rebuildable — Run history stays resolvable), not the Source's
	// projection re-derivable from a row that no longer belongs here.
	if _, err := tx.Exec(ctx, `DELETE FROM graph.source WHERE id = $1::uuid`, sourceID); err != nil {
		return 0, fmt.Errorf("graph: delete rehomed source: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("graph: commit complete rehome: %w", err)
	}
	return tombstoned, nil
}

// GetSourceHome reads a Source's home Cell and re-home seal state (ADR-0045) —
// the local half of the fleet-home resolver. found=false when this Cell has no
// row for the Source (it may be homed on a peer, or greenfield). Cheap indexed
// point read; never touches peers.
func (s *Store) GetSourceHome(ctx context.Context, name string) (cell string, rehomingTo string, found bool, err error) {
	var scell, srehome *string
	err = s.pool.QueryRow(ctx,
		`SELECT cell, rehoming_to FROM graph.source WHERE name = $1`, name,
	).Scan(&scell, &srehome)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("graph: get source home: %w", err)
	}
	if scell != nil {
		cell = *scell
	}
	if srehome != nil {
		rehomingTo = *srehome
	}
	return cell, rehomingTo, true, nil
}

// AbortRehome un-seals a Source whose re-home failed BEFORE the destination
// adopted it (must-fix 4: after a committed adopt the move is roll-forward-only,
// never aborted). It clears rehoming_to and bumps home_epoch again so a late,
// stale adopt at the old epoch is rejected by AdoptSource's epoch fence. The
// Source resumes projecting on its original Cell. Idempotent.
func (s *Store) AbortRehome(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE graph.source SET rehoming_to = NULL, home_epoch = home_epoch + 1
		WHERE name = $1 AND rehoming_to IS NOT NULL`, name)
	if err != nil {
		return fmt.Errorf("graph: abort rehome: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Nothing sealed — either unknown or already settled; not an error (the
		// abort is a best-effort compensation the workflow may retry).
		return nil
	}
	return nil
}

// WriteRehomeStuckFinding opens (or refreshes) the §1.8 live-surface Finding that
// a Source re-home is in progress and not yet complete (must-fix 5): a partition,
// an unreachable destination, or a Connector not yet deployed on the destination
// leaves the Source sealed (frozen, zero writers) — a blocked state that must be
// visible on the Findings surface, not just a reconcile log line. Auto-resolved
// by ResolveRehomeFinding on complete or abort. framework 'rehome', one row per
// Source via the (baseline,target) partial unique index.
func (s *Store) WriteRehomeStuckFinding(ctx context.Context, name, destCell, severity string, detail []byte) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO graph.finding
			(baseline, target, status, severity, framework, consecutive_drifted, diff, opened_at)
		VALUES ($1, $2, 'open', $3, $4, 1, $5, now())
		ON CONFLICT (baseline, target) WHERE status <> 'resolved'
		DO UPDATE SET severity = excluded.severity, diff = excluded.diff, last_observed = now()`,
		"rehome:"+name, "source:"+name, severity, types.FindingRehomeStuck, detail)
	if err != nil {
		return fmt.Errorf("graph: write rehome-stuck finding: %w", err)
	}
	return nil
}

// ResolveRehomeFinding resolves the stuck-seal Finding for a Source once its
// re-home terminates (complete or abort). resolved_reason records which.
func (s *Store) ResolveRehomeFinding(ctx context.Context, name, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE graph.finding
		SET status = 'resolved', resolved_at = now(), consecutive_drifted = 0, resolved_reason = $1
		WHERE framework = $2 AND target = $3 AND status <> 'resolved'`,
		reason, types.FindingRehomeStuck, "source:"+name)
	if err != nil {
		return fmt.Errorf("graph: resolve rehome finding: %w", err)
	}
	return nil
}
