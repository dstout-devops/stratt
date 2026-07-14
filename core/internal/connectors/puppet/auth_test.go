package puppet

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// certFixture is a CA plus a server cert (for 127.0.0.1) and a client cert, all
// signed by the CA — the minimum to prove mTLS both ways.
type certFixture struct {
	caFile, clientCertFile, clientKeyFile string
	serverCert                            tls.Certificate
	caPool                                *x509.CertPool
}

func genCerts(t *testing.T) certFixture {
	t.Helper()
	dir := t.TempDir()

	caKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "stratt-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)

	sign := func(cn string, ipSAN bool, eku x509.ExtKeyUsage) (certDER []byte, key *rsa.PrivateKey) {
		key, _ = rsa.GenerateKey(rand.Reader, 2048)
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(time.Now().UnixNano()),
			Subject:      pkix.Name{CommonName: cn},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{eku},
		}
		if ipSAN {
			tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		}
		certDER, err = x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		return certDER, key
	}

	srvDER, srvKey := sign("puppetdb.test", true, x509.ExtKeyUsageServerAuth)
	cliDER, cliKey := sign("stratt", false, x509.ExtKeyUsageClientAuth)

	caFile := filepath.Join(dir, "ca.pem")
	writePEM(t, caFile, "CERTIFICATE", caDER)
	clientCertFile := filepath.Join(dir, "client.pem")
	writePEM(t, clientCertFile, "CERTIFICATE", cliDER)
	clientKeyFile := filepath.Join(dir, "client-key.pem")
	writePEM(t, clientKeyFile, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(cliKey))

	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	return certFixture{
		caFile: caFile, clientCertFile: clientCertFile, clientKeyFile: clientKeyFile,
		serverCert: tls.Certificate{Certificate: [][]byte{srvDER}, PrivateKey: srvKey},
		caPool:     pool,
	}
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestPuppetDBMutualTLS proves the stdlib mTLS client path: a client presenting
// its cert connects to a server that requires+verifies it, and a client without
// the cert is refused. This proves auth with no real PuppetDB (harness-only
// build) — bog-standard TLS, zero third-party crypto (contrast: Chef's go-chef).
func TestPuppetDBMutualTLS(t *testing.T) {
	fx := genCerts(t)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{fx.serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    fx.caPool,
	}
	srv.StartTLS()
	defer srv.Close()

	// With the client cert: the connector's mTLS client connects.
	cfg := Config{BaseURL: srv.URL, CertFile: fx.clientCertFile, KeyFile: fx.clientKeyFile, CAFile: fx.caFile, SourceName: "pdb"}
	client, err := cfg.httpClient()
	if err != nil {
		t.Fatalf("build mTLS client: %v", err)
	}
	res, err := client.Get(srv.URL + "/pdb/query/v4/inventory")
	if err != nil {
		t.Fatalf("mTLS client rejected by server requiring client cert: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %s", res.Status)
	}

	// Without a client cert (trusts the CA but presents nothing): server refuses.
	noCert := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: fx.caPool}}}
	if _, err := noCert.Get(srv.URL + "/pdb/query/v4/inventory"); err == nil {
		t.Fatal("server accepted a client presenting no certificate; mTLS not enforced")
	}
}
