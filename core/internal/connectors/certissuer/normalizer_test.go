package certissuer

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

// mkCA builds a self-signed CA (its own certificate + signing key).
func mkCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Stratt Dev Root CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(87600 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	crt, _ := x509.ParseCertificate(der)
	return crt, key, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// mkLeaf builds a leaf PEM signed by the given CA, so its Issuer CN is the CA's.
func mkLeaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, cn string, notAfter time.Time) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		DNSNames:     []string{cn},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestNormalizeLeafCert(t *testing.T) {
	ca, caKey, _ := mkCA(t)
	notAfter := time.Now().Add(720 * time.Hour).UTC().Truncate(time.Second)
	c := Cert{Serial: "2a:9a:e1", PEM: mkLeaf(t, ca, caKey, "web.stratt.test", notAfter)}
	up, ok, err := normalizeCert(c)
	if err != nil || !ok {
		t.Fatalf("leaf must project: ok=%v err=%v", ok, err)
	}
	if up.Kind != "cert" || up.IdentityKeys["cert.serial"] != "2a:9a:e1" {
		t.Fatalf("identity: %+v", up)
	}
	var id struct {
		CommonName, Issuer string
		DNSNames           []string
	}
	if err := json.Unmarshal(up.Facets["cert.identity"], &id); err != nil {
		t.Fatal(err)
	}
	if id.CommonName != "web.stratt.test" || id.Issuer != "Stratt Dev Root CA" || len(id.DNSNames) != 1 {
		t.Fatalf("cert.identity: %+v", id)
	}
	var exp struct{ NotAfter string }
	if err := json.Unmarshal(up.Facets["cert.expiry"], &exp); err != nil {
		t.Fatal(err)
	}
	if exp.NotAfter != notAfter.Format(time.RFC3339) {
		t.Fatalf("cert.expiry.notAfter = %q, want %q", exp.NotAfter, notAfter.Format(time.RFC3339))
	}
}

func TestNormalizeSkipsCA(t *testing.T) {
	_, _, caPEM := mkCA(t)
	c := Cert{Serial: "ca01", PEM: caPEM}
	_, ok, err := normalizeCert(c)
	if err != nil {
		t.Fatalf("CA parse must not error: %v", err)
	}
	if ok {
		t.Fatal("CA cert must be skipped, not projected (§2.4: the authority is not an estate leaf)")
	}
}

func TestNormalizeBadPEM(t *testing.T) {
	if _, _, err := normalizeCert(Cert{Serial: "x", PEM: "not a pem"}); err == nil {
		t.Fatal("a serial whose PEM does not parse must error (skipped loudly, §1.8)")
	}
}
