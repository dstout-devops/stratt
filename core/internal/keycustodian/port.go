package keycustodian

import (
	"context"
	"encoding/json"
	"fmt"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// portCustodian is a KeyCustodian provider reached over the sovereign plugin port
// (ADR-0100): it dials a plugin's WrapKey/UnwrapKey RPCs. The plugin wraps the DEK in
// its KMS (e.g. OpenBao Transit) — the KEK never leaves the KMS. This core-side
// custodian adds the self-describing {provider, domain, keyVersion} envelope so a
// wrapped DEK is portable, residency-bound, and rewrappable-to-local (§1.3 eject).
type portCustodian struct {
	client   pluginv1.PluginServiceClient
	identity string // provider identity stamped in wrapped DEKs (e.g. "openbao-transit")
}

// NewPort builds a Custodian backed by a plugin's KeyCustodian capability. identity is
// the provider tag written into wrapped DEKs (must be stable — reads dispatch on it).
func NewPort(client pluginv1.PluginServiceClient, identity string) Custodian {
	return &portCustodian{client: client, identity: identity}
}

func (p *portCustodian) Identity() string { return p.identity }

func (p *portCustodian) Wrap(ctx context.Context, domain string, dek []byte) ([]byte, error) {
	resp, err := p.client.WrapKey(ctx, &pluginv1.WrapKeyRequest{Domain: domain, Dek: dek})
	if err != nil {
		return nil, fmt.Errorf("keycustodian: port wrap (domain %q): %w", domain, err)
	}
	return json.Marshal(wrappedKey{Provider: p.identity, Domain: domain, KeyVersion: int(resp.GetKeyVersion()), Wrapped: resp.GetWrapped()})
}

func (p *portCustodian) Unwrap(ctx context.Context, wrapped []byte) ([]byte, error) {
	var wk wrappedKey
	if err := json.Unmarshal(wrapped, &wk); err != nil {
		return nil, fmt.Errorf("keycustodian: malformed wrapped key: %w", err)
	}
	if wk.Provider != p.identity {
		return nil, fmt.Errorf("keycustodian: %q custodian cannot unwrap a %q-wrapped DEK (domain %q)", p.identity, wk.Provider, wk.Domain)
	}
	resp, err := p.client.UnwrapKey(ctx, &pluginv1.UnwrapKeyRequest{Wrapped: wk.Wrapped, Domain: wk.Domain})
	if err != nil {
		return nil, fmt.Errorf("keycustodian: port unwrap (domain %q): %w", wk.Domain, err)
	}
	return resp.GetDek(), nil
}

// muxCustodian wraps with a primary custodian and UNWRAPS by dispatching on the wrapped
// DEK's self-described provider — so blobs from different providers (the local floor and
// a KMS) coexist, and a domain can migrate (rewrap) between them without a flag day
// (ADR-0100). This is what makes the self-describing envelope load-bearing.
type muxCustodian struct {
	primary    Custodian
	byProvider map[string]Custodian
}

// NewMux builds a Custodian that wraps with primary and unwraps via the provider that
// wrote each blob (primary + others). The local floor is always included so
// local-wrapped and legacy blobs stay readable after a KMS is enabled.
func NewMux(primary Custodian, others ...Custodian) Custodian {
	m := &muxCustodian{primary: primary, byProvider: map[string]Custodian{primary.Identity(): primary}}
	for _, c := range others {
		m.byProvider[c.Identity()] = c
	}
	return m
}

func (m *muxCustodian) Identity() string { return m.primary.Identity() }

func (m *muxCustodian) Wrap(ctx context.Context, domain string, dek []byte) ([]byte, error) {
	return m.primary.Wrap(ctx, domain, dek)
}

func (m *muxCustodian) Unwrap(ctx context.Context, wrapped []byte) ([]byte, error) {
	prov, err := peekProvider(wrapped)
	if err != nil {
		return nil, err
	}
	c, ok := m.byProvider[prov]
	if !ok {
		return nil, fmt.Errorf("keycustodian: no custodian for provider %q — is that KMS configured and reachable for this domain?", prov)
	}
	return c.Unwrap(ctx, wrapped)
}

// peekProvider reads only the provider tag from a wrapped DEK (to route Unwrap).
func peekProvider(wrapped []byte) (string, error) {
	var wk struct {
		Provider string `json:"p"`
	}
	if err := json.Unmarshal(wrapped, &wk); err != nil {
		return "", fmt.Errorf("keycustodian: malformed wrapped key: %w", err)
	}
	return wk.Provider, nil
}
