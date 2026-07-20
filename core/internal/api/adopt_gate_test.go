package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/authz"
)

// TestAdoptObjectAuthzGate proves the ADR-0086 §1.6 authz gate: a Principal WITHOUT the
// `adopt` grant on the object's Source is denied at the door (403), before any catalog
// resolve or deep-read. fakeAuthz.Check denies (returns false).
func TestAdoptObjectAuthzGate(t *testing.T) {
	s := &Server{Authz: fakeAuthz{}}
	body := `{"kind":"ansible.template","identity":"ctrl-a/10","endpoint":"http://awx.example","credentialRef":"awx-token"}`
	ctx := authz.WithPrincipal(context.Background(), "alice", authz.KindHuman)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/adoptions", strings.NewReader(body)).WithContext(ctx)
	w := httptest.NewRecorder()

	s.AdoptObject(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("ungranted adopt must be 403, got %d: %s", w.Code, w.Body.String())
	}
}

// TestAdoptObjectValidatesBody: a request missing required fields fails at the door (400),
// before authz — no principal, no store touched.
func TestAdoptObjectValidatesBody(t *testing.T) {
	s := &Server{Authz: fakeAuthz{}}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/adoptions", strings.NewReader(`{"kind":"ansible.template"}`))
	w := httptest.NewRecorder()

	s.AdoptObject(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing identity/endpoint must be 400, got %d: %s", w.Code, w.Body.String())
	}
}
