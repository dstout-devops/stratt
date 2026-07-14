package awxfacade

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/authz"
)

// TestFacadeSCIMGate proves the /api/v2 compat surface inherits the SCIM
// deactivation block (ADR-0035, charter-guardian Violation 1): a deactivated
// human is denied via the façade too; service/agent are never gated.
func TestFacadeSCIMGate(t *testing.T) {
	ctx := context.Background()
	deactivated := map[string]bool{"fired-admin": true}
	f := &Facade{cfg: Config{
		DevPrincipalHeader: true,
		SCIMGate: func(_ context.Context, id string) error {
			if deactivated[id] {
				return errors.New("deactivated")
			}
			return nil
		},
	}}
	hdr := func(id, kind string) http.Header {
		h := http.Header{}
		h.Set("X-Stratt-Principal", id)
		if kind != "" {
			h.Set("X-Stratt-Principal-Kind", kind)
		}
		return h
	}

	if id, _, err := f.resolve(ctx, hdr("alice", "")); err != nil || id != "alice" {
		t.Fatalf("active human via façade: id=%q err=%v", id, err)
	}
	if _, _, err := f.resolve(ctx, hdr("fired-admin", "")); err == nil {
		t.Fatal("façade must deny a deactivated human (offboarding symmetry)")
	}
	if id, _, err := f.resolve(ctx, hdr("fired-admin", authz.KindService)); err != nil || id != "fired-admin" {
		t.Fatalf("service must not be gated via façade: id=%q err=%v", id, err)
	}
}
