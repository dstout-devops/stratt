package graph

import (
	"context"
	"encoding/json"
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
	// Implements maps a GRANTED capability class to the Action name the provider's Manifest
	// advertises as its implementation (ADR-0140 D1). Nil/empty is lawful and usual — only
	// classes reached through a resolve Action appear; a class routed through a per-kind
	// Workflow map has none. Core never parses these names, only carries them.
	Implements map[string]string
	// Basis is HOW the verdict was reached (ADR-0138 D5): "manifest" (the running plugin was
	// dialed and its own advertisement checked) or "declaration" (dial-less — corroborated
	// against the declared mechanisms because no Manifest exists to fetch). Empty when not
	// verified. Recorded because two verdicts that both read verified=true are not equally
	// strong, and a surface that hides which is which hides diagnosis (§1.8).
	Basis string
}

// UpsertProviderVerification records (idempotently) a provider's verification outcome and the
// class→Action implementations its Manifest advertised for the classes it was granted.
func (s *Store) UpsertProviderVerification(ctx context.Context, kind, name string, verified bool, reason string, implements map[string]string, basis string) error {
	if implements == nil {
		implements = map[string]string{}
	}
	raw, err := json.Marshal(implements)
	if err != nil {
		return fmt.Errorf("graph: marshal capability implements %s/%s: %w", kind, name, err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO graph.capability_provider (provider_kind, provider_name, verified, reason, implements, basis, checked_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (provider_kind, provider_name)
		DO UPDATE SET verified = excluded.verified, reason = excluded.reason,
		              implements = excluded.implements, basis = excluded.basis, checked_at = now()`,
		kind, name, verified, reason, raw, basis)
	if err != nil {
		return fmt.Errorf("graph: upsert capability provider %s/%s: %w", kind, name, err)
	}
	return nil
}

// ListProviderVerifications returns every recorded provider verification.
func (s *Store) ListProviderVerifications(ctx context.Context) ([]ProviderVerification, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT provider_kind, provider_name, verified, reason, implements, basis
		 FROM graph.capability_provider ORDER BY provider_kind, provider_name`)
	if err != nil {
		return nil, fmt.Errorf("graph: list capability providers: %w", err)
	}
	defer rows.Close()
	var out []ProviderVerification
	for rows.Next() {
		var p ProviderVerification
		var raw []byte
		if err := rows.Scan(&p.Kind, &p.Name, &p.Verified, &p.Reason, &raw, &p.Basis); err != nil {
			return nil, fmt.Errorf("graph: scan capability provider: %w", err)
		}
		if err := json.Unmarshal(raw, &p.Implements); err != nil {
			return nil, fmt.Errorf("graph: scan capability implements %s/%s: %w", p.Kind, p.Name, err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProviderVerification returns one provider's verification outcome, ok=false if none is
// recorded (the provider declares no capability, or has not yet been verified).
func (s *Store) GetProviderVerification(ctx context.Context, kind, name string) (ProviderVerification, bool, error) {
	p := ProviderVerification{Kind: kind, Name: name}
	var raw []byte
	err := s.pool.QueryRow(ctx,
		`SELECT verified, reason, implements, basis FROM graph.capability_provider WHERE provider_kind = $1 AND provider_name = $2`,
		kind, name).Scan(&p.Verified, &p.Reason, &raw, &p.Basis)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProviderVerification{}, false, nil
	}
	if err != nil {
		return ProviderVerification{}, false, fmt.Errorf("graph: get capability provider %s/%s: %w", kind, name, err)
	}
	if err := json.Unmarshal(raw, &p.Implements); err != nil {
		return ProviderVerification{}, false, fmt.Errorf("graph: get capability implements %s/%s: %w", kind, name, err)
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
