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
	issue      Issued          // what Issue returns
	issueErr   error
	revoked    []string // serials passed to Revoke
	revocation int64    // what Revoke returns
}

func (f *fakeCA) ListSerials(context.Context) ([]string, error) { return f.serials, nil }

func (f *fakeCA) GetCert(_ context.Context, serial string) (Cert, error) {
	return f.certs[serial], nil
}

func (f *fakeCA) Issue(context.Context, string, string, string) (Issued, error) {
	return f.issue, f.issueErr
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
	for _, ns := range []string{"cert.identity", "cert.expiry"} {
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
}

// TestInvokeIssue proves the Action half of the port for the issue op: Issue → a
// terminal InvokeResponse carrying the typed outputs (serial, commonName) AND the
// new cert as an ObservedEntity. Result is set ONLY on the terminal event.
func TestInvokeIssue(t *testing.T) {
	f := &fakeCA{issue: Issued{Serial: "ff:ee", PEM: "NEWPEM", Expiration: 1893456000}}

	args, _ := json.Marshal(certParams{Role: "stratt-dev", CommonName: "app.stratt.test", TTL: "720h"})
	req := &pluginv1.InvokeRequest{Action: actionIssue, Args: &pluginv1.Payload{Bytes: args}}
	stream := &captureStream[pluginv1.InvokeResponse]{ctx: context.Background()}
	if err := newServer(t, f).Invoke(req, stream); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if len(stream.sent) < 2 {
		t.Fatalf("expected a progress event then a terminal event, got %d", len(stream.sent))
	}

	// Only the final message is terminal and carries the Result.
	for i, resp := range stream.sent {
		last := i == len(stream.sent)-1
		if resp.GetEvent().GetTerminal() != last {
			t.Errorf("message %d terminal=%v, want %v", i, resp.GetEvent().GetTerminal(), last)
		}
		if !last && resp.GetResult() != nil {
			t.Errorf("non-terminal message %d must not carry Result", i)
		}
	}

	term := stream.sent[len(stream.sent)-1]
	if !term.GetEvent().GetOk() {
		t.Fatal("terminal event must be ok")
	}
	res := term.GetResult()
	if res == nil {
		t.Fatal("terminal message must carry Result")
	}
	var out map[string]any
	if err := json.Unmarshal(res.GetOutputs().GetBytes(), &out); err != nil {
		t.Fatalf("outputs: %v", err)
	}
	if out["serial"] != "ff:ee" || out["commonName"] != "app.stratt.test" {
		t.Fatalf("outputs: %v", out)
	}
	// The token and private key must NEVER appear in the outputs (§2.5).
	if _, bad := out["token"]; bad {
		t.Fatal("outputs must not carry the CLM token")
	}
	if _, bad := out["privateKey"]; bad {
		t.Fatal("outputs must not carry the private key")
	}
	if len(res.GetEntities()) != 1 {
		t.Fatalf("issue must project the new cert entity, got %d", len(res.GetEntities()))
	}
	ent := res.GetEntities()[0]
	if ent.GetKind() != "cert" || ent.GetIdentityKeys()["cert.serial"] != "ff:ee" {
		t.Fatalf("entity: kind=%q identity=%v", ent.GetKind(), ent.GetIdentityKeys())
	}
	if ent.GetLabels()["cert.commonName"] != "app.stratt.test" {
		t.Fatalf("entity labels: %v", ent.GetLabels())
	}
	if res.GetOutputContract().GetSchemaId() != "actions/certissuer/issue.output" {
		t.Errorf("output contract: %q", res.GetOutputContract().GetSchemaId())
	}
}

// TestInvokeRevoke proves the revoke op: Revoke by serial → a terminal ok
// InvokeResponse. It projects no Entity (the Syncer tombstones the revoked cert
// as absent on its next poll).
func TestInvokeRevoke(t *testing.T) {
	f := &fakeCA{revocation: 1783968900}

	args, _ := json.Marshal(certParams{Serial: "ff:ee"})
	req := &pluginv1.InvokeRequest{Action: actionRevoke, Args: &pluginv1.Payload{Bytes: args}}
	stream := &captureStream[pluginv1.InvokeResponse]{ctx: context.Background()}
	if err := newServer(t, f).Invoke(req, stream); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	term := stream.sent[len(stream.sent)-1]
	if !term.GetEvent().GetTerminal() || !term.GetEvent().GetOk() {
		t.Fatalf("revoke must end in a terminal ok event: %+v", term.GetEvent())
	}
	if len(f.revoked) != 1 || f.revoked[0] != "ff:ee" {
		t.Fatalf("revoke must call the CLM with the target serial, got %v", f.revoked)
	}
	res := term.GetResult()
	if res == nil {
		t.Fatal("terminal message must carry Result")
	}
	if len(res.GetEntities()) != 0 {
		t.Errorf("revoke must project no entity, got %d", len(res.GetEntities()))
	}
	var out map[string]any
	if err := json.Unmarshal(res.GetOutputs().GetBytes(), &out); err != nil {
		t.Fatalf("outputs: %v", err)
	}
	if out["serial"] != "ff:ee" {
		t.Fatalf("outputs: %v", out)
	}
}

// TestInvokeDryRunTouchesNothing — a dry-run plans without any CLM write: no
// Issue/Revoke call, a terminal ok, and no bindable outputs/entity (§2.2).
func TestInvokeDryRunTouchesNothing(t *testing.T) {
	f := &fakeCA{}
	args, _ := json.Marshal(certParams{Role: "stratt-dev", CommonName: "plan.stratt.test"})
	req := &pluginv1.InvokeRequest{Action: actionIssue, Args: &pluginv1.Payload{Bytes: args}, DryRun: true}
	stream := &captureStream[pluginv1.InvokeResponse]{ctx: context.Background()}
	if err := newServer(t, f).Invoke(req, stream); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if f.issue != (Issued{}) || len(f.revoked) != 0 {
		t.Fatal("dry-run must not touch the CLM")
	}
	term := stream.sent[len(stream.sent)-1]
	if !term.GetEvent().GetTerminal() || !term.GetEvent().GetOk() {
		t.Fatalf("dry-run must end terminal ok: %+v", term.GetEvent())
	}
	if term.GetResult().GetOutputs() != nil || len(term.GetResult().GetEntities()) != 0 {
		t.Error("a dry-run plan must carry no bindable outputs and no entity")
	}
}

// TestInvokeUnknownActionRejected — a content-blind selector that names no shipped
// Action is rejected, never guessed. The empty selector is NOT the sole Action
// here (certissuer is multi-op).
func TestInvokeUnknownActionRejected(t *testing.T) {
	for _, action := range []string{"", "certissuer/delete-ca"} {
		stream := &captureStream[pluginv1.InvokeResponse]{ctx: context.Background()}
		err := newServer(t, &fakeCA{}).Invoke(&pluginv1.InvokeRequest{Action: action}, stream)
		if err == nil {
			t.Errorf("action %q must be rejected", action)
		}
	}
}

// TestGetManifest — the SYNCER class advertises OBSERVE + INVOKE, the 2 cert facet
// namespaces, the cert.serial tombstone scheme, and THREE ActionDecls with their
// idempotent/dry-run flags and input/output contract ids.
func TestGetManifest(t *testing.T) {
	resp, err := newServer(t, &fakeCA{}).GetManifest(context.Background(), &pluginv1.GetManifestRequest{})
	if err != nil {
		t.Fatal(err)
	}
	m := resp.GetManifest()
	if m.GetClass() != pluginv1.PluginClass_PLUGIN_CLASS_SYNCER {
		t.Errorf("class = %v", m.GetClass())
	}
	verbs := map[pluginv1.Verb]bool{}
	for _, v := range m.GetVerbs() {
		verbs[v] = true
	}
	if !verbs[pluginv1.Verb_VERB_OBSERVE] || !verbs[pluginv1.Verb_VERB_INVOKE] {
		t.Errorf("verbs = %v, want OBSERVE+INVOKE", m.GetVerbs())
	}
	if len(m.GetContracts()) != 2 {
		t.Errorf("expected 2 facet contracts, got %d", len(m.GetContracts()))
	}
	if len(m.GetTombstoneSchemes()) != 1 || m.GetTombstoneSchemes()[0] != "cert.serial" {
		t.Errorf("tombstone schemes = %v", m.GetTombstoneSchemes())
	}
	if len(m.GetActions()) != 3 {
		t.Fatalf("expected 3 actions, got %d", len(m.GetActions()))
	}
	want := map[string]struct {
		idempotent bool
		in, out    string
	}{
		actionIssue:  {false, "actions/certissuer/issue.input", "actions/certissuer/issue.output"},
		actionRenew:  {false, "actions/certissuer/renew.input", "actions/certissuer/renew.output"},
		actionRevoke: {true, "actions/certissuer/revoke.input", "actions/certissuer/revoke.output"},
	}
	for _, a := range m.GetActions() {
		w, ok := want[a.GetName()]
		if !ok {
			t.Errorf("unexpected action %q", a.GetName())
			continue
		}
		if !a.GetDryRunnable() {
			t.Errorf("%s must be dry_runnable", a.GetName())
		}
		if a.GetIdempotent() != w.idempotent {
			t.Errorf("%s idempotent=%v, want %v", a.GetName(), a.GetIdempotent(), w.idempotent)
		}
		if a.GetInput().GetSchemaId() != w.in || a.GetOutput().GetSchemaId() != w.out {
			t.Errorf("%s contracts: in=%q out=%q", a.GetName(), a.GetInput().GetSchemaId(), a.GetOutput().GetSchemaId())
		}
	}
}
