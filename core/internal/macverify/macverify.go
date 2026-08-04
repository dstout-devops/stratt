// Package macverify answers one question — does this signature match these bytes? — WITHOUT the
// core ever holding the key that would let it answer directly (ADR-0164 D2).
//
// ── WHY THIS IS A PORT CALL AND NOT TWENTY LINES OF crypto/hmac ─────────────────────────────────
//
// Verifying an inbound webhook signature requires the shared secret itself, not a hash of it.
// ADR-0052 states, as a property it explicitly declined to weaken, that **the core never holds
// credential material, even transiently** — so the twenty-line version is not a shortcut weighed
// against a purist objection; it contradicts an Accepted decision at its central point, in the
// daemon that terminates untrusted inbound HTTP. The key stays where keys live and the answer comes
// back as a boolean, which is ADR-0100's KeyCustodian argument with one noun changed.
//
// What the core DOES handle is what the caller presented — unavoidable for any inbound
// authentication, and the same thing the token path has always done.
package macverify

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
	"github.com/dstout-devops/stratt/types"
)

// Verifier answers whether a presented signature matches a body.
type Verifier interface {
	// Verify reports whether sig authenticates body under the declared key. An error means the
	// question could not be ASKED (the provider is unreachable, the algorithm is unknown); false
	// with no error means it was asked and answered no. Callers must keep those apart: one is a
	// refusal of this caller, the other is a refusal of everyone until an operator acts (§1.8).
	Verify(ctx context.Context, spec types.VerifySpec, body, sig []byte) (bool, error)
}

// portVerifier reaches a plugin advertising the `macverifier` capability.
type portVerifier struct{ client pluginv1.PluginServiceClient }

// NewPort builds a Verifier backed by a plugin's MACVerifier capability.
func NewPort(client pluginv1.PluginServiceClient) Verifier { return &portVerifier{client: client} }

func (p *portVerifier) Verify(ctx context.Context, spec types.VerifySpec, body, sig []byte) (bool, error) {
	resp, err := p.client.VerifyMAC(ctx, &pluginv1.VerifyMACRequest{
		Body: body, Signature: sig, KeyRef: spec.KeyRef, Algorithm: spec.Algorithm,
	})
	if err != nil {
		return false, fmt.Errorf("macverify: provider could not answer for key %q: %w", spec.KeyRef, err)
	}
	return resp.GetValid(), nil
}

// DecodeSignature turns the header value a source presented into raw MAC bytes: strip the declared
// prefix, decode the declared encoding.
//
// The prefix is REQUIRED when declared, not merely trimmed — accepting a value without it would
// widen what is accepted past what the declaration says, the same rule the token header follows.
func DecodeSignature(spec types.VerifySpec, presented string) ([]byte, error) {
	raw, ok := strings.CutPrefix(presented, spec.Prefix)
	if !ok {
		return nil, fmt.Errorf("signature does not carry the declared %q prefix", spec.Prefix)
	}
	if raw == "" {
		return nil, fmt.Errorf("no signature presented in %s", spec.Header)
	}
	switch spec.Encoding {
	case types.SignatureBase64:
		return base64.StdEncoding.DecodeString(raw)
	default: // hex is the default and by far the common case
		return hex.DecodeString(raw)
	}
}
