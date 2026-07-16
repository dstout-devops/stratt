package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/authz"
	"github.com/dstout-devops/stratt/core/internal/cellrouter"
)

// TestResolvePrincipalSCIMGate proves the request-time deactivation block
// (ADR-0035): a deactivated human is denied, while service/agent and
// unknown-to-SCIM subjects are never gated.
func TestResolvePrincipalSCIMGate(t *testing.T) {
	ctx := context.Background()
	deactivated := map[string]bool{"fired-admin": true}
	s := &Server{
		DevPrincipalHeader: true,
		SCIMGate: func(_ context.Context, id string) error {
			if deactivated[id] {
				return errors.New("deactivated")
			}
			return nil
		},
	}
	hdr := func(id, kind string) http.Header {
		h := http.Header{}
		h.Set("X-Stratt-Principal", id)
		if kind != "" {
			h.Set("X-Stratt-Principal-Kind", kind)
		}
		return h
	}

	// Active human resolves normally.
	if id, _, err := s.ResolvePrincipal(ctx, hdr("alice", "")); err != nil || id != "alice" {
		t.Fatalf("active human: id=%q err=%v", id, err)
	}
	// Deactivated human is denied.
	if _, _, err := s.ResolvePrincipal(ctx, hdr("fired-admin", "")); err == nil {
		t.Fatal("deactivated human must be denied")
	}
	// A SERVICE principal is never gated — even sharing the deactivated id.
	if id, _, err := s.ResolvePrincipal(ctx, hdr("fired-admin", authz.KindService)); err != nil || id != "fired-admin" {
		t.Fatalf("service must not be gated: id=%q err=%v", id, err)
	}

	// With no gate configured, nothing is blocked.
	s.SCIMGate = nil
	if id, _, err := s.ResolvePrincipal(ctx, hdr("fired-admin", "")); err != nil || id != "fired-admin" {
		t.Fatalf("no gate: id=%q err=%v", id, err)
	}
}

// TestResolvePrincipalFanoutAssertion proves a verified cross-Cell fan-out
// asserts the acting Principal at the ONE identity seam (ADR-0044 slice 5, §1.6):
// it is honored only with a secret configured AND the fan-out header, and it is
// still subject to the SCIM offboarding gate (a deactivated human is denied even
// on a forwarded child launch).
func TestResolvePrincipalFanoutAssertion(t *testing.T) {
	ctx := context.Background()
	deactivated := map[string]bool{"fired-admin": true}
	s := &Server{
		CellSecret: []byte("fleet-secret"),
		SCIMGate: func(_ context.Context, id string) error {
			if deactivated[id] {
				return errors.New("deactivated")
			}
			return nil
		},
	}
	fanout := func(id string) http.Header {
		h := http.Header{}
		h.Set(cellrouter.FanoutHeader, "1")
		h.Set("X-Stratt-Principal", id)
		return h
	}

	// A verified fan-out asserts the Principal (DevPrincipalHeader is OFF).
	if id, kind, err := s.ResolvePrincipal(ctx, fanout("alice")); err != nil || id != "alice" || kind != authz.KindHuman {
		t.Fatalf("verified fan-out must assert the Principal: id=%q kind=%q err=%v", id, kind, err)
	}
	// The SCIM gate still applies to a forwarded child launch.
	if _, _, err := s.ResolvePrincipal(ctx, fanout("fired-admin")); err == nil {
		t.Fatal("a deactivated human must be denied even on a forwarded child launch")
	}
	// Without a secret (single-Cell), the assertion header is NOT trusted.
	s.CellSecret = nil
	if id, _, err := s.ResolvePrincipal(ctx, fanout("alice")); err != nil || id != "" {
		t.Fatalf("no secret ⇒ the fan-out assertion must be untrusted, got id=%q err=%v", id, err)
	}
}
