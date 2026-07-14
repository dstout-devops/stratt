package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/authz"
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
