package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/dstout-devops/stratt/types"
)

// Notifications (ADR-0027) are CaC-only: the desired-state engine is the sole
// writer of Sinks and Subscriptions, mirroring Emitters/Triggers. Deliveries
// are the runtime status surface, written by the notifier.

// UpsertNotifySink writes one declared Sink.
func (s *Store) UpsertNotifySink(ctx context.Context, sink types.Sink) error {
	spec, err := json.Marshal(sink)
	if err != nil {
		return fmt.Errorf("graph: marshal notify sink: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO graph.notify_sink (name, kind, spec)
		VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET kind = excluded.kind, spec = excluded.spec`,
		sink.Name, sink.Kind, spec)
	if err != nil {
		return fmt.Errorf("graph: upsert notify sink: %w", err)
	}
	return nil
}

// GetNotifySink returns one Sink declaration.
func (s *Store) GetNotifySink(ctx context.Context, name string) (types.Sink, error) {
	var spec []byte
	err := s.pool.QueryRow(ctx,
		`SELECT spec FROM graph.notify_sink WHERE name = $1`, name).Scan(&spec)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.Sink{}, fmt.Errorf("%w: notify sink %s", ErrNotFound, name)
	}
	if err != nil {
		return types.Sink{}, fmt.Errorf("graph: get notify sink: %w", err)
	}
	var sink types.Sink
	if err := json.Unmarshal(spec, &sink); err != nil {
		return sink, fmt.Errorf("graph: decode notify sink spec: %w", err)
	}
	return sink, nil
}

// ListNotifySinks returns every Sink declaration, ordered by name.
func (s *Store) ListNotifySinks(ctx context.Context) ([]types.Sink, error) {
	rows, err := s.pool.Query(ctx, `SELECT spec FROM graph.notify_sink ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("graph: list notify sinks: %w", err)
	}
	defer rows.Close()
	var out []types.Sink
	for rows.Next() {
		var spec []byte
		if err := rows.Scan(&spec); err != nil {
			return nil, fmt.Errorf("graph: list notify sinks: %w", err)
		}
		var sink types.Sink
		if err := json.Unmarshal(spec, &sink); err != nil {
			return nil, fmt.Errorf("graph: decode notify sink spec: %w", err)
		}
		out = append(out, sink)
	}
	return out, rows.Err()
}

// DeleteNotifySink removes one Sink declaration.
func (s *Store) DeleteNotifySink(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM graph.notify_sink WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("graph: delete notify sink: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: notify sink %s", ErrNotFound, name)
	}
	return nil
}

// UpsertSubscription writes one declared Subscription.
func (s *Store) UpsertSubscription(ctx context.Context, sub types.Subscription) error {
	spec, err := json.Marshal(sub)
	if err != nil {
		return fmt.Errorf("graph: marshal subscription: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO graph.notify_subscription (name, spec)
		VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET spec = excluded.spec`,
		sub.Name, spec)
	if err != nil {
		return fmt.Errorf("graph: upsert subscription: %w", err)
	}
	return nil
}

// GetSubscription returns one Subscription declaration.
func (s *Store) GetSubscription(ctx context.Context, name string) (types.Subscription, error) {
	var spec []byte
	err := s.pool.QueryRow(ctx,
		`SELECT spec FROM graph.notify_subscription WHERE name = $1`, name).Scan(&spec)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.Subscription{}, fmt.Errorf("%w: subscription %s", ErrNotFound, name)
	}
	if err != nil {
		return types.Subscription{}, fmt.Errorf("graph: get subscription: %w", err)
	}
	var sub types.Subscription
	if err := json.Unmarshal(spec, &sub); err != nil {
		return sub, fmt.Errorf("graph: decode subscription spec: %w", err)
	}
	return sub, nil
}

// ListSubscriptions returns every Subscription declaration, ordered by name.
func (s *Store) ListSubscriptions(ctx context.Context) ([]types.Subscription, error) {
	rows, err := s.pool.Query(ctx, `SELECT spec FROM graph.notify_subscription ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("graph: list subscriptions: %w", err)
	}
	defer rows.Close()
	var out []types.Subscription
	for rows.Next() {
		var spec []byte
		if err := rows.Scan(&spec); err != nil {
			return nil, fmt.Errorf("graph: list subscriptions: %w", err)
		}
		var sub types.Subscription
		if err := json.Unmarshal(spec, &sub); err != nil {
			return nil, fmt.Errorf("graph: decode subscription spec: %w", err)
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// DeleteSubscription removes one Subscription declaration.
func (s *Store) DeleteSubscription(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM graph.notify_subscription WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("graph: delete subscription: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: subscription %s", ErrNotFound, name)
	}
	return nil
}

// RecordDelivery persists one delivery attempt — the §1.8 status surface.
func (s *Store) RecordDelivery(ctx context.Context, d types.NotifyDelivery) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO graph.notify_delivery
			(notice_kind, subject, subscription, sink, status, detail, run_id)
		VALUES ($1, $2, $3, $4, $5, nullif($6, ''), nullif($7, '')::uuid)`,
		d.NoticeKind, d.Subject, d.Subscription, d.Sink, d.Status, d.Detail, d.RunID)
	if err != nil {
		return fmt.Errorf("graph: record delivery: %w", err)
	}
	return nil
}

// ListDeliveries returns recent delivery attempts, newest first (capped).
func (s *Store) ListDeliveries(ctx context.Context, limit int) ([]types.NotifyDelivery, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, notice_kind, subject, subscription, sink, status, coalesce(detail, ''), at
		FROM graph.notify_delivery ORDER BY at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("graph: list deliveries: %w", err)
	}
	defer rows.Close()
	var out []types.NotifyDelivery
	for rows.Next() {
		var d types.NotifyDelivery
		if err := rows.Scan(&d.ID, &d.NoticeKind, &d.Subject, &d.Subscription,
			&d.Sink, &d.Status, &d.Detail, &d.At); err != nil {
			return nil, fmt.Errorf("graph: list deliveries: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
