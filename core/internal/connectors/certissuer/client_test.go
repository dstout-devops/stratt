package certissuer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeCLM stands in for the OpenBao PKI REST surface exercised by the client
// (the shapes verified against the live server in the ADR-0030 probe).
func fakeCLM(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "tok" {
			http.Error(w, `{"errors":["permission denied"]}`, http.StatusForbidden)
			return
		}
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/certs") && r.URL.Query().Get("list") == "true":
			w.Write([]byte(`{"data":{"keys":["2a:9a","3d:40"]}}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/cert/2a:9a"):
			w.Write([]byte(`{"data":{"certificate":"-----BEGIN CERTIFICATE-----\nX\n-----END CERTIFICATE-----","revocation_time":0}}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/cert/3d:40"):
			w.Write([]byte(`{"data":{"certificate":"PEM","revocation_time":1783968824}}`))
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/issue/"):
			w.Write([]byte(`{"data":{"serial_number":"ff:ee","certificate":"NEWPEM"}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/revoke"):
			w.Write([]byte(`{"data":{"revocation_time":1783968900}}`))
		default:
			http.Error(w, `{"errors":["no route"]}`, http.StatusNotFound)
		}
	}))
}

func TestClientRoundTrip(t *testing.T) {
	srv := fakeCLM(t)
	defer srv.Close()
	ctx := context.Background()
	c := NewClient(srv.URL, "tok", "pki")

	serials, err := c.ListSerials(ctx)
	if err != nil || len(serials) != 2 {
		t.Fatalf("list: %v %v", serials, err)
	}
	live, err := c.GetCert(ctx, "2a:9a")
	if err != nil || live.Revoked || !strings.HasPrefix(live.PEM, "-----BEGIN") {
		t.Fatalf("get live: %+v %v", live, err)
	}
	revoked, err := c.GetCert(ctx, "3d:40")
	if err != nil || !revoked.Revoked {
		t.Fatalf("get revoked must report Revoked=true: %+v %v", revoked, err)
	}
	iss, err := c.Issue(ctx, "stratt-dev", "web.stratt.test", "720h")
	if err != nil || iss.Serial != "ff:ee" {
		t.Fatalf("issue: %+v %v", iss, err)
	}
	if err := c.Revoke(ctx, "ff:ee"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
}

func TestClientAuthError(t *testing.T) {
	srv := fakeCLM(t)
	defer srv.Close()
	c := NewClient(srv.URL, "wrong", "pki")
	if _, err := c.ListSerials(context.Background()); err == nil {
		t.Fatal("a 403 must surface as an error (never a silent empty list, §1.8)")
	}
}
