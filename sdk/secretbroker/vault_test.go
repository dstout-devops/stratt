package secretbroker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// kvServer is a minimal OpenBao/Vault KV stand-in: it checks the token and returns
// the v1 or v2 response shape for the configured path.
func kvServer(t *testing.T, wantToken, path string, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Vault-Token"); got != wantToken {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Path != path {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

// TestWithMaterial_VaultKVv2 proves the vault backend reads ONLY the authorized fields
// from a KV v2 secret (the nested data wrapper) and hands them under their logical
// names — the vault analogue of the k8s ResolvedKey test (ADR-0094).
func TestWithMaterial_VaultKVv2(t *testing.T) {
	// password carries an escaped quote and slash to exercise the byte-level unquote.
	srv := kvServer(t, "dev-token", "/v1/secret/data/myapp",
		`{"data":{"data":{"username":"admin","password":"p@ss/w\"rd","unrelated":"nope"},"metadata":{"version":3}}}`)
	defer srv.Close()

	r := New(fake.NewSimpleClientset(), "plugin-ns", WithVault(srv.URL, "dev-token"))
	ref := &pluginv1.ResolvedRef{
		Vault: &pluginv1.VaultCoords{Mount: "secret", Path: "myapp", KvV2: true},
		Keys: []*pluginv1.ResolvedKey{
			{Key: "username", Name: "user"},
			{Key: "password", Name: "pass"},
		},
	}

	var gotUser, gotPass string
	if err := r.WithMaterial(context.Background(), ref, func(m Material) error {
		gotUser = m.GetString("user")
		gotPass = m.GetString("pass")
		if m.Get("unrelated") != nil {
			t.Fatal("only the authorized ResolvedKey set must resolve, not every KV field")
		}
		return nil
	}); err != nil {
		t.Fatalf("WithMaterial: %v", err)
	}
	if gotUser != "admin" || gotPass != `p@ss/w"rd` {
		t.Fatalf("resolved vault material wrong: user=%q pass=%q", gotUser, gotPass)
	}
}

// TestWithMaterial_VaultKVv1 proves the v1 (unwrapped) read shape.
func TestWithMaterial_VaultKVv1(t *testing.T) {
	srv := kvServer(t, "tok", "/v1/kv/app", `{"data":{"token":"abc123"}}`)
	defer srv.Close()

	r := New(fake.NewSimpleClientset(), "ns", WithVault(srv.URL, "tok"))
	ref := &pluginv1.ResolvedRef{
		Vault: &pluginv1.VaultCoords{Mount: "kv", Path: "app", KvV2: false},
		Keys:  []*pluginv1.ResolvedKey{{Key: "token", Name: "token"}},
	}
	if err := r.WithMaterial(context.Background(), ref, func(m Material) error {
		if m.GetString("token") != "abc123" {
			t.Fatalf("v1 read wrong: %q", m.GetString("token"))
		}
		return nil
	}); err != nil {
		t.Fatalf("WithMaterial: %v", err)
	}
}

// TestWithMaterial_VaultZeroizes proves MF-B holds for the vault backend too: the
// []byte handed out is zeroized after use — the whole point of decoding KV values into
// []byte rather than an (un-zeroizable) Go string (ADR-0094).
func TestWithMaterial_VaultZeroizes(t *testing.T) {
	srv := kvServer(t, "tok", "/v1/secret/data/s", `{"data":{"data":{"token":"s3cr3t"}}}`)
	defer srv.Close()

	r := New(fake.NewSimpleClientset(), "ns", WithVault(srv.URL, "tok"))
	ref := &pluginv1.ResolvedRef{
		Vault: &pluginv1.VaultCoords{Mount: "secret", Path: "s", KvV2: true},
		Keys:  []*pluginv1.ResolvedKey{{Key: "token", Name: "token"}},
	}
	var leaked []byte
	if err := r.WithMaterial(context.Background(), ref, func(m Material) error {
		leaked = m.Get("token")
		if string(leaked) != "s3cr3t" {
			t.Fatalf("pre-zero material wrong: %q", string(leaked))
		}
		return nil
	}); err != nil {
		t.Fatalf("WithMaterial: %v", err)
	}
	for _, b := range leaked {
		if b != 0 {
			t.Fatalf("MF-B: vault material must be zeroized after use, found non-zero byte %d", b)
		}
	}
}

// TestWithMaterial_VaultNoClientFailsClosed proves a vault-coordinate ResolvedRef at a
// Resolver with NO vault backend configured fails closed — the plugin never reaches
// for material it cannot confine (MF-C spirit, ADR-0094).
func TestWithMaterial_VaultNoClientFailsClosed(t *testing.T) {
	r := New(fake.NewSimpleClientset(), "ns") // no WithVault
	ref := &pluginv1.ResolvedRef{
		Vault: &pluginv1.VaultCoords{Mount: "secret", Path: "x", KvV2: true},
		Keys:  []*pluginv1.ResolvedKey{{Key: "token", Name: "token"}},
	}
	if err := r.WithMaterial(context.Background(), ref, func(Material) error {
		t.Fatal("use must not run with vault coordinates but no vault backend")
		return nil
	}); err == nil {
		t.Fatal("vault coordinates with no configured backend must fail closed")
	}
}

// TestJSONStringToBytes covers the zeroizable decode: fast path, standard escapes,
// a \u escape, and rejection of a non-string KV value.
func TestJSONStringToBytes(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{`"plain"`, "plain", false},
		{`"a/b+c=="`, "a/b+c==", false}, // base64-ish token, no escapes
		{`"tab\tnl\nquote\"slash\/back\\"`, "tab\tnl\nquote\"slash/back\\", false},
		{`"Aé"`, "Aé", false},       // \u BMP escapes → A, é
		{`123`, "", true},           // number, not a string
		{`"unterminated`, "", true}, // not a closed JSON string
	}
	for _, c := range cases {
		got, err := jsonStringToBytes([]byte(c.in))
		if c.wantErr {
			if err == nil {
				t.Fatalf("jsonStringToBytes(%s): expected error, got %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("jsonStringToBytes(%s): %v", c.in, err)
		}
		if string(got) != c.want {
			t.Fatalf("jsonStringToBytes(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}
