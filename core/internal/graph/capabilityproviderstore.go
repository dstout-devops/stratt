package graph

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Capability-provider verification (ADR-0104 D1) is a RUNTIME projection: the
// connectorregistry's leader-only verification reconcile is the sole writer. It records,
// per declared provider, whether its dialed Manifest advertised the capability classes it
// was declared to `provides`. It is store-visible so capability resolution counts only
// VERIFIED providers identically on every replica (the D3 property).

// ProviderVerification is one provider's verification outcome.
type ProviderVerification struct {
	Kind     string // "connector" | "actuator"
	Name     string
	Verified bool
	Reason   string // phantom/dial reason when !Verified; "" when verified
}

// UpsertProviderVerification records (idempotently) a provider's verification outcome.
func (s *Store) UpsertProviderVerification(ctx context.Context, kind, name string, verified bool, reason string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO graph.capability_provider (provider_kind, provider_name, verified, reason, checked_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (provider_kind, provider_name)
		DO UPDATE SET verified = excluded.verified, reason = excluded.reason, checked_at = now()`,
		kind, name, verified, reason)
	if err != nil {
		return fmt.Errorf("graph: upsert capability provider %s/%s: %w", kind, name, err)
	}
	return nil
}

// ListProviderVerifications returns every recorded provider verification.
func (s *Store) ListProviderVerifications(ctx context.Context) ([]ProviderVerification, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT provider_kind, provider_name, verified, reason FROM graph.capability_provider ORDER BY provider_kind, provider_name`)
	if err != nil {
		return nil, fmt.Errorf("graph: list capability providers: %w", err)
	}
	defer rows.Close()
	var out []ProviderVerification
	for rows.Next() {
		var p ProviderVerification
		if err := rows.Scan(&p.Kind, &p.Name, &p.Verified, &p.Reason); err != nil {
			return nil, fmt.Errorf("graph: scan capability provider: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProviderVerification returns one provider's verification outcome, ok=false if none is
// recorded (the provider declares no capability, or has not yet been verified).
func (s *Store) GetProviderVerification(ctx context.Context, kind, name string) (ProviderVerification, bool, error) {
	p := ProviderVerification{Kind: kind, Name: name}
	err := s.pool.QueryRow(ctx,
		`SELECT verified, reason FROM graph.capability_provider WHERE provider_kind = $1 AND provider_name = $2`,
		kind, name).Scan(&p.Verified, &p.Reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProviderVerification{}, false, nil
	}
	if err != nil {
		return ProviderVerification{}, false, fmt.Errorf("graph: get capability provider %s/%s: %w", kind, name, err)
	}
	return p, true, nil
}

// DeleteProviderVerification removes a provider's verification row (it is no longer a
// declared provider). Idempotent.
func (s *Store) DeleteProviderVerification(ctx context.Context, kind, name string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM graph.capability_provider WHERE provider_kind = $1 AND provider_name = $2`, kind, name)
	if err != nil {
		return fmt.Errorf("graph: delete capability provider %s/%s: %w", kind, name, err)
	}
	return nil
}
