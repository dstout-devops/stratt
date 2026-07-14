package graph

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/dstout-devops/stratt/core/internal/audit"
	"github.com/dstout-devops/stratt/types"
)

// RecordAudit appends one event to the audit stream (charter §1.6, ADR-0034).
// The hot path is a plain INSERT — the seq is assigned by the DB and the hash
// chain is filled in later by the sealer, so recording never bottlenecks the
// full access log. detail must never carry secret material (§2.5).
func (s *Store) RecordAudit(ctx context.Context, e types.AuditEvent) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO audit.event (principal_id, principal_kind, action, object, outcome, detail)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		e.PrincipalID, e.PrincipalKind, e.Action, e.Object, e.Outcome, nullJSON(e.Detail))
	if err != nil {
		return fmt.Errorf("graph: record audit: %w", err)
	}
	return nil
}

const auditColumns = `seq, at, principal_id, principal_kind, action, object, outcome, detail, prev_hash, hash`

// LatestAuditForObject returns the most recent audit event for a given
// (action, object) pair — e.g. the last access.recertify attestation of a View
// (ADR-0036). The audit stream is the durable record of an attestation, so
// "last attested" is a query over it, not a second table (§1.6). Returns
// found=false when the object has never seen that action.
func (s *Store) LatestAuditForObject(ctx context.Context, action, object string) (types.AuditEvent, bool, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+auditColumns+`
		FROM audit.event
		WHERE action = $1 AND object = $2
		ORDER BY seq DESC LIMIT 1`, action, object)
	e, err := scanAudit(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.AuditEvent{}, false, nil
	}
	if err != nil {
		return types.AuditEvent{}, false, fmt.Errorf("graph: latest audit for object: %w", err)
	}
	return e, true, nil
}

// ListAudit returns audit events in seq order after `since` (a cursor),
// optionally filtered by principal and/or action. Ascending seq = forward
// pagination: pass the last seq seen as the next `since`.
func (s *Store) ListAudit(ctx context.Context, principal, action string, since int64, limit int) ([]types.AuditEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+auditColumns+`
		FROM audit.event
		WHERE seq > $1 AND ($2 = '' OR principal_id = $2) AND ($3 = '' OR action = $3)
		ORDER BY seq ASC LIMIT $4`, since, principal, action, limit)
	if err != nil {
		return nil, fmt.Errorf("graph: list audit: %w", err)
	}
	defer rows.Close()
	var out []types.AuditEvent
	for rows.Next() {
		e, err := scanAudit(rows)
		if err != nil {
			return nil, fmt.Errorf("graph: list audit: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func scanAudit(row pgx.Row) (types.AuditEvent, error) {
	var e types.AuditEvent
	err := row.Scan(&e.Seq, &e.At, &e.PrincipalID, &e.PrincipalKind, &e.Action, &e.Object, &e.Outcome, &e.Detail, &e.PrevHash, &e.Hash)
	return e, err
}

// SealPending chains the unsealed tail of the ledger in seq order and advances
// the seal head (ADR-0034). It is the single writer of prev_hash/hash: a
// FOR UPDATE lock on the one seal_head row serializes concurrent sealers so the
// chain is linear. Returns the number of events sealed this pass.
func (s *Store) SealPending(ctx context.Context) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var headSeq int64
	var headHash []byte
	if err := tx.QueryRow(ctx, `SELECT seq, hash FROM audit.seal_head WHERE id LIMIT 1 FOR UPDATE`).Scan(&headSeq, &headHash); err != nil {
		return 0, fmt.Errorf("graph: seal head: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT seq, at, principal_id, principal_kind, action, object, outcome, detail
		FROM audit.event WHERE hash IS NULL ORDER BY seq ASC`)
	if err != nil {
		return 0, fmt.Errorf("graph: unsealed: %w", err)
	}
	type sealed struct {
		seq        int64
		prev, hash []byte
	}
	var batch []sealed
	prev := headHash
	lastSeq, lastHash := headSeq, headHash
	for rows.Next() {
		var e types.AuditEvent
		if err := rows.Scan(&e.Seq, &e.At, &e.PrincipalID, &e.PrincipalKind, &e.Action, &e.Object, &e.Outcome, &e.Detail); err != nil {
			rows.Close()
			return 0, err
		}
		h := audit.ChainHash(prev, e)
		batch = append(batch, sealed{seq: e.Seq, prev: prev, hash: h})
		prev, lastSeq, lastHash = h, e.Seq, h
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(batch) == 0 {
		return 0, nil
	}

	for _, b := range batch {
		if _, err := tx.Exec(ctx, `UPDATE audit.event SET prev_hash=$1, hash=$2 WHERE seq=$3`, b.prev, b.hash, b.seq); err != nil {
			return 0, fmt.Errorf("graph: seal seq %d: %w", b.seq, err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE audit.seal_head SET seq=$1, hash=$2 WHERE id`, lastSeq, lastHash); err != nil {
		return 0, fmt.Errorf("graph: advance seal head: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(batch), nil
}

// VerifyAudit walks the sealed prefix of the ledger and recomputes the hash
// chain (ADR-0034, §1.8). It catches altered content (a row whose stored hash
// no longer matches ChainHash), a broken link (prev_hash not equal to the
// predecessor's hash — e.g. a deleted middle row), and a truncated tail (the
// sealed rows do not reach seal_head). Returns the first offending seq.
func (s *Store) VerifyAudit(ctx context.Context) (types.AuditVerification, error) {
	var headSeq int64
	var headHash []byte
	if err := s.pool.QueryRow(ctx, `SELECT seq, hash FROM audit.seal_head WHERE id LIMIT 1`).Scan(&headSeq, &headHash); err != nil {
		return types.AuditVerification{}, fmt.Errorf("graph: seal head: %w", err)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT seq, at, principal_id, principal_kind, action, object, outcome, detail, prev_hash, hash
		FROM audit.event WHERE hash IS NOT NULL ORDER BY seq ASC`)
	if err != nil {
		return types.AuditVerification{}, fmt.Errorf("graph: verify audit: %w", err)
	}
	defer rows.Close()

	var prev []byte
	var count, lastSeq int64
	var lastHash []byte
	for rows.Next() {
		e, err := scanAudit(rows)
		if err != nil {
			return types.AuditVerification{}, err
		}
		if !bytes.Equal(e.PrevHash, prev) {
			return types.AuditVerification{SealedThrough: headSeq, Events: count, FirstBadSeq: e.Seq,
				Reason: "broken chain link (prev_hash mismatch — a preceding event was altered or removed)"}, nil
		}
		if want := audit.ChainHash(e.PrevHash, e); !bytes.Equal(e.Hash, want) {
			return types.AuditVerification{SealedThrough: headSeq, Events: count, FirstBadSeq: e.Seq,
				Reason: "stored hash does not match content (event altered)"}, nil
		}
		prev, count, lastSeq, lastHash = e.Hash, count+1, e.Seq, e.Hash
	}
	if err := rows.Err(); err != nil {
		return types.AuditVerification{}, err
	}
	if lastSeq != headSeq || !bytes.Equal(lastHash, headHash) {
		return types.AuditVerification{SealedThrough: headSeq, Events: count, FirstBadSeq: headSeq,
			Reason: "sealed events do not reach the seal head (the tail was truncated)"}, nil
	}
	return types.AuditVerification{OK: true, SealedThrough: headSeq, Events: count}, nil
}

// nullJSON returns nil for an empty raw message so an absent detail stores as
// SQL NULL (not a literal "null" jsonb), keeping the hash of detail-less events
// stable.
func nullJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// ── forward cursor + delivery status (ADR-0034 Commit 2, tables in 00019) ──

// ForwardBatch returns the next in-order batch of audit events after a Sink's
// committed offset — the at-least-once egress read (the forwarder commits the
// offset only after the SIEM acks).
func (s *Store) ForwardBatch(ctx context.Context, since int64, limit int) ([]types.AuditEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+auditColumns+`
		FROM audit.event WHERE seq > $1 ORDER BY seq ASC LIMIT $2`, since, limit)
	if err != nil {
		return nil, fmt.Errorf("graph: forward batch: %w", err)
	}
	defer rows.Close()
	var out []types.AuditEvent
	for rows.Next() {
		e, err := scanAudit(rows)
		if err != nil {
			return nil, fmt.Errorf("graph: forward batch: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetForwardOffset returns a Sink's committed through_seq (0 if none yet).
func (s *Store) GetForwardOffset(ctx context.Context, sink string) (int64, error) {
	var through int64
	err := s.pool.QueryRow(ctx, `SELECT through_seq FROM audit.forward_offset WHERE sink=$1`, sink).Scan(&through)
	if err != nil {
		if err == pgx.ErrNoRows {
			return 0, nil
		}
		return 0, fmt.Errorf("graph: forward offset: %w", err)
	}
	return through, nil
}

// CommitForwardOffset advances a Sink's committed offset. It only moves
// forward: a redelivered lower ack (e.g. after a crash) never rewinds a Sink
// that already progressed.
func (s *Store) CommitForwardOffset(ctx context.Context, sink string, throughSeq int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO audit.forward_offset (sink, through_seq, at) VALUES ($1, $2, now())
		ON CONFLICT (sink) DO UPDATE SET through_seq = GREATEST(audit.forward_offset.through_seq, EXCLUDED.through_seq), at = now()`,
		sink, throughSeq)
	if err != nil {
		return fmt.Errorf("graph: commit forward offset: %w", err)
	}
	return nil
}

// RecordForwardDelivery appends one egress delivery outcome (§1.8). detail
// never carries event bodies or secret material.
func (s *Store) RecordForwardDelivery(ctx context.Context, d types.ForwardDelivery) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO audit.forward_delivery (sink, through_seq, count, status, detail)
		VALUES ($1, $2, $3, $4, $5)`, d.Sink, d.ThroughSeq, d.Count, d.Status, d.Detail)
	if err != nil {
		return fmt.Errorf("graph: record forward delivery: %w", err)
	}
	return nil
}

// ListForwardDeliveries returns recent egress outcomes, newest first.
func (s *Store) ListForwardDeliveries(ctx context.Context, sink string, limit int) ([]types.ForwardDelivery, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT sink, through_seq, count, status, detail, at
		FROM audit.forward_delivery WHERE ($1 = '' OR sink = $1)
		ORDER BY at DESC LIMIT $2`, sink, limit)
	if err != nil {
		return nil, fmt.Errorf("graph: list forward deliveries: %w", err)
	}
	defer rows.Close()
	var out []types.ForwardDelivery
	for rows.Next() {
		var d types.ForwardDelivery
		if err := rows.Scan(&d.Sink, &d.ThroughSeq, &d.Count, &d.Status, &d.Detail, &d.At); err != nil {
			return nil, fmt.Errorf("graph: list forward deliveries: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
