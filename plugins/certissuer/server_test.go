package certissuer

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"testing"
	"time"

	"google.golang.org/grpc"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// fakeCA stands in for the Vault-compatible PKI CLM — it lets us exercise the
// plugin's content-expertise in isolation, no live CLM (the ADR-0046
// module-isolation point: the plugin is its own test unit, importing neither core
// nor Postgres). It mirrors the in-tree tests' fake-CLM pattern.
type fakeCA struct {
	certs      map[string]Cert // serial -> read cert
	serials    []string        // enumeration order
	current    *CurrentCert    // what Current returns (the observed live cert for the CN)
	signed     Issued          // what Sign returns
	signErr    error
	signedCSR  string   // the CSR passed to Sign (proves born-on-target)
	revoked    []string // serials passed to Revoke
	revocation int64    // what Revoke returns
}

func (f *fakeCA) ListSerials(context.Context) ([]string, error) { return f.serials, nil }

func (f *fakeCA) GetCert(_ context.Context, serial string) (Cert, error) {
	return f.certs[serial], nil
}

func (f *fakeCA) Current(context.Context, string) (*CurrentCert, error) { return f.current, nil }

func (f *fakeCA) Sign(_ context.Context, _, csrPEM, _ string) (Issued, error) {
	f.signedCSR = csrPEM
	return f.signed, f.signErr
}

func (f *fakeCA) Revoke(_ context.Context, serial string) (int64, error) {
	f.revoked = append(f.revoked, serial)
	return f.revocation, nil
}

// captureStream is a fake grpc.ServerStreamingServer[T] recording sent messages —
// the awsec2/vcenter test posture, exercising the server through the port surface
// without a live gRPC connection.
type captureStream[T any] struct {
	grpc.ServerStream
	ctx  context.Context
	sent []*T
}

func (s *captureStream[T]) Context() context.Context { return s.ctx }
func (s *captureStream[T]) Send(m *T) error          { s.sent = append(s.sent, m); return nil }

func newServer(t *testing.T, ca CA) *Server {
	t.Helper()
	s := NewServer(Config{Addr: "http://clm.test", Token: "tok"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.newCA = func(context.Context) (CA, error) { return ca, nil }
	return s
}

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
func mkLeaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, cn string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(720 * time.Hour),
		DNSNames:     []string{cn},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// TestObserveEmitsCerts proves the Syncer half of the port: enumerate → live leaf
// cert ObservedEntities with the identity, label, and facet blobs the wire carries,
// and the full_sync_complete boundary. The CA cert and revoked certs are skipped
// (they count as absent → the host tombstones).
func TestObserveEmitsCerts(t *testing.T) {
	ca, caKey, caPEM := mkCA(t)
	live := Cert{Serial: "2a:9a", PEM: mkLeaf(t, ca, caKey, "web.stratt.test")}
	authority := Cert{Serial: "ca01", PEM: caPEM}
	revoked := Cert{Serial: "3d:40", PEM: mkLeaf(t, ca, caKey, "old.stratt.test"), Revoked: true}

	f := &fakeCA{
		serials: []string{"2a:9a", "ca01", "3d:40"},
		certs:   map[string]Cert{"2a:9a": live, "ca01": authority, "3d:40": revoked},
	}

	stream := &captureStream[pluginv1.ObserveResponse]{ctx: context.Background()}
	if err := newServer(t, f).Observe(&pluginv1.ObserveRequest{}, stream); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if len(stream.sent) != 1 {
		t.Fatalf("expected one ObserveResponse, got %d", len(stream.sent))
	}
	resp := stream.sent[0]
	if !resp.GetFullSyncComplete() {
		t.Error("full sync must set full_sync_complete for the tombstone boundary")
	}
	if len(resp.GetEntities()) != 1 {
		t.Fatalf("expected one live leaf cert (CA + revoked skipped), got %d", len(resp.GetEntities()))
	}
	e := resp.GetEntities()[0]
	if e.GetKind() != "cert" {
		t.Errorf("kind = %q, want cert", e.GetKind())
	}
	if e.GetIdentityKeys()["cert.serial"] != "2a:9a" {
		t.Errorf("identity: %v", e.GetIdentityKeys())
	}
	if e.GetLabels()["cert.commonName"] != "web.stratt.test" {
		t.Errorf("labels: %v", e.GetLabels())
	}
	for _, ns := range []string{"cert.identity", "cert.expiry", "identity.credential"} {
		if len(e.GetFacets()[ns]) == 0 {
			t.Errorf("missing facet %q", ns)
		}
	}
	var id struct {
		CommonName, Issuer string
		DNSNames           []string
	}
	if err := json.Unmarshal(e.GetFacets()["cert.identity"], &id); err != nil {
		t.Fatalf("cert.identity: %v", err)
	}
	if id.CommonName != "web.stratt.test" || id.Issuer != "Stratt Dev Root CA" || len(id.DNSNames) != 1 {
		t.Fatalf("cert.identity: %+v", id)
	}
	// ADR-0079 slice 2: the cert also carries the cross-form identity.credential —
	// scheme=cert, the attested subject, and the expiry the cross-form query keys on.
	var cred struct {
		Scheme, SubjectName, Issuer, NotAfter string
		SubjectAltNames                       []string
	}
	if err := json.Unmarshal(e.GetFacets()["identity.credential"], &cred); err != nil {
		t.Fatalf("identity.credential: %v", err)
	}
	if cred.Scheme != "cert" || cred.SubjectName != "web.stratt.test" || cred.NotAfter == "" {
		t.Fatalf("identity.credential: %+v", cred)
	}
}

// planApply drives Plan then Apply with a desired JSON, returning the Apply's
// terminal ApplyResponse.
func applyDesired(t *testing.T, f *fakeCA, desiredJSON string, dryRun bool) *pluginv1.ApplyResponse {
	t.Helper()
	stream := &captureStream[pluginv1.ApplyResponse]{ctx: context.Background()}
	err := newServer(t, f).Apply(&pluginv1.ApplyRequest{Desired: &pluginv1.Payload{Bytes: []byte(desiredJSON)}, DryRun: dryRun}, stream)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, m := range stream.sent {
		if m.GetEvent().GetTerminal() {
			return m
		}
	}
	t.Fatal("no terminal ApplyResponse")
	return nil
}

// TestPlan_IssueRenewNoop proves the plugin-owned semantic diff (ADR-0050 §2):
// no cert → issue; within renewBefore → renew; healthy → noop (empty).
func TestPlan_IssueRenewNoop(t *testing.T) {
	plan := func(f *fakeCA, desired string) *pluginv1.PlanResponse {
		p, err := newServer(t, f).Plan(context.Background(), &pluginv1.PlanRequest{Desired: &pluginv1.Payload{Bytes: []byte(desired)}})
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		return p
	}
	// no live cert → issue (not empty).
	if p := plan(&fakeCA{current: nil}, `{"commonName":"web.test","role":"web"}`); p.GetEmpty() {
		t.Fatal("no cert must plan a non-empty (issue) diff")
	}
	// cert expiring within the window → renew (not empty).
	soon := &fakeCA{current: &CurrentCert{Serial: "aa:bb", NotAfter: time.Now().Add(24 * time.Hour)}}
	if p := plan(soon, `{"commonName":"web.test","role":"web","renewBefore":"168h"}`); p.GetEmpty() {
		t.Fatal("a cert within renewBefore must plan a non-empty (renew) diff")
	}
	// healthy cert outside the window → noop (empty == converged).
	healthy := &fakeCA{current: &CurrentCert{Serial: "cc:dd", NotAfter: time.Now().Add(2000 * time.Hour)}}
	if p := plan(healthy, `{"commonName":"web.test","role":"web","renewBefore":"168h"}`); !p.GetEmpty() {
		t.Fatal("a healthy cert must plan empty (converged)")
	}
}

// TestApply_IssueSignsCSR proves Apply signs the TARGET's CSR (born-on-target, never
// /issue), writes back the new cert Entity, and folds CHANGED.
func TestApply_IssueSignsCSR(t *testing.T) {
	f := &fakeCA{current: nil, signed: Issued{Serial: "ff:ee"}}
	term := applyDesired(t, f, `{"commonName":"web.test","role":"web","csr":"CSR-PEM-FROM-TARGET"}`, false)
	if f.signedCSR != "CSR-PEM-FROM-TARGET" {
		t.Fatalf("Apply must sign the target's CSR (born-on-target), got %q", f.signedCSR)
	}
	if term.GetResult().GetStatus() != pluginv1.ItemResult_STATUS_CHANGED || !term.GetEvent().GetOk() {
		t.Fatalf("a fresh issue must fold CHANGED+ok: %+v", term.GetResult())
	}
	if len(term.GetWriteBack()) != 1 || term.GetWriteBack()[0].GetIdentityKeys()["cert.serial"] != "ff:ee" {
		t.Fatalf("Apply must write back the new cert Entity: %+v", term.GetWriteBack())
	}
}

// TestApply_NoCSRFailsVisibly proves the key-delivery invariant (ADR-0050 §3): a
// convergence that would sign without a target CSR is REFUSED, never a silent
// /issue-and-discard-the-key.
func TestApply_NoCSRFailsVisibly(t *testing.T) {
	f := &fakeCA{current: nil, signed: Issued{Serial: "x"}}
	term := applyDesired(t, f, `{"commonName":"web.test","role":"web"}`, false)
	if term.GetResult().GetStatus() != pluginv1.ItemResult_STATUS_FAILED || term.GetEvent().GetOk() {
		t.Fatalf("issue/renew without a CSR must fail visibly (born-on-target), got %+v", term.GetResult())
	}
	if f.signedCSR != "" {
		t.Fatal("must not call Sign without a CSR")
	}
}

// TestApply_NoopConverged proves a healthy cert converges to OK with no CLM write.
func TestApply_NoopConverged(t *testing.T) {
	f := &fakeCA{current: &CurrentCert{Serial: "cc:dd", NotAfter: time.Now().Add(2000 * time.Hour)}}
	term := applyDesired(t, f, `{"commonName":"web.test","role":"web","renewBefore":"168h","csr":"X"}`, false)
	if term.GetResult().GetStatus() != pluginv1.ItemResult_STATUS_OK {
		t.Fatalf("a healthy cert must converge to OK, got %+v", term.GetResult())
	}
	if f.signedCSR != "" || len(f.revoked) != 0 {
		t.Fatal("noop must touch no CLM state")
	}
}

// TestApply_RenewRevokesSuperseded proves renew signs a new cert AND revokes the
// old serial (converge to exactly one valid cert per CN, ADR-0050 §5).
func TestApply_RenewRevokesSuperseded(t *testing.T) {
	f := &fakeCA{current: &CurrentCert{Serial: "old:11", NotAfter: time.Now().Add(24 * time.Hour)}, signed: Issued{Serial: "new:22"}}
	term := applyDesired(t, f, `{"commonName":"web.test","role":"web","renewBefore":"168h","csr":"CSR"}`, false)
	if term.GetResult().GetStatus() != pluginv1.ItemResult_STATUS_CHANGED {
		t.Fatalf("renew must fold CHANGED, got %+v", term.GetResult())
	}
	if len(f.revoked) != 1 || f.revoked[0] != "old:11" {
		t.Fatalf("renew must revoke the superseded serial, got %+v", f.revoked)
	}
}

// TestDestroy_RevokesAndTombstones proves the gated Destroy path: revoke the cert
// for the CN + emit a GoneEntity so the graph reflects it immediately.
func TestDestroy_RevokesAndTombstones(t *testing.T) {
	f := &fakeCA{current: &CurrentCert{Serial: "kill:99", NotAfter: time.Now().Add(500 * time.Hour)}}
	stream := &captureStream[pluginv1.DestroyResponse]{ctx: context.Background()}
	if err := newServer(t, f).Destroy(&pluginv1.DestroyRequest{Desired: &pluginv1.Payload{Bytes: []byte(`{"commonName":"web.test","role":"web"}`)}}, stream); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	term := stream.sent[len(stream.sent)-1]
	if len(f.revoked) != 1 || f.revoked[0] != "kill:99" {
		t.Fatalf("destroy must revoke the CN's cert, got %+v", f.revoked)
	}
	if len(term.GetGone()) != 1 || term.GetGone()[0].GetValue() != "kill:99" {
		t.Fatalf("destroy must tombstone the cert Entity, got %+v", term.GetGone())
	}
}

// TestGetManifest_ActuatorVerbs proves the manifest advertises the reconcile verbs.
func TestGetManifest_ActuatorVerbs(t *testing.T) {
	m, _ := newServer(t, &fakeCA{}).GetManifest(context.Background(), &pluginv1.GetManifestRequest{})
	verbs := map[pluginv1.Verb]bool{}
	for _, v := range m.GetManifest().GetVerbs() {
		verbs[v] = true
	}
	if !verbs[pluginv1.Verb_VERB_PLAN] || !verbs[pluginv1.Verb_VERB_APPLY] || !verbs[pluginv1.Verb_VERB_DESTROY] || !verbs[pluginv1.Verb_VERB_OBSERVE] {
		t.Fatalf("certissuer must advertise OBSERVE+PLAN+APPLY+DESTROY, got %v", m.GetManifest().GetVerbs())
	}
}
