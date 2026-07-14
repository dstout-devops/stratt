package salt

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/connectors/salt/saltsim"
)

// TestSaltEauthTokenFlow proves the salt-api eauth flow against saltsim: login
// mints a token, an authed call is accepted, and a call without the token is
// refused (401). Proves auth with no real Salt master (harness-only build).
func TestSaltEauthTokenFlow(t *testing.T) {
	sim := saltsim.New()
	srv := httptest.NewServer(sim.Handler())
	defer srv.Close()

	client := newSaltClient(Config{APIURL: srv.URL, Username: "stratt", Password: "pw", SourceName: "salt-test"})
	ctx := context.Background()
	if err := client.login(ctx); err != nil {
		t.Fatalf("login: %v", err)
	}
	tok, err := client.authToken(ctx)
	if err != nil || tok == "" {
		t.Fatalf("authToken: %q %v", tok, err)
	}

	lowstate := []byte(`{"client":"runner","fun":"cache.grains","tgt":"*","tgt_type":"glob"}`)

	// With the token: accepted.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/", bytes.NewReader(lowstate))
	req.Header.Set("X-Auth-Token", tok)
	res, err := client.http.Do(req)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("authed runner call: status=%v err=%v", res, err)
	}
	_ = res.Body.Close()

	// Without a token: refused.
	req2, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/", bytes.NewReader(lowstate))
	res2, err := client.http.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	_ = res2.Body.Close()
	if res2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("untokened call must be 401, got %s", res2.Status)
	}
}
