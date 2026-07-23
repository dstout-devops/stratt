package keycustodian

import (
	"bytes"
	"context"
	"crypto/rand"
	"testing"

	"google.golang.org/grpc"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// fakeClient is a pluginv1.PluginServiceClient whose WrapKey/UnwrapKey simulate a KMS
// (a reversible transform standing in for Transit). The embedded nil interface panics
// on any other method — the portCustodian only calls these two.
type fakeClient struct {
	pluginv1.PluginServiceClient
	lastWrapDomain, lastUnwrapDomain string
}

func (f *fakeClient) WrapKey(_ context.Context, in *pluginv1.WrapKeyRequest, _ ...grpc.CallOption) (*pluginv1.WrapKeyResponse, error) {
	f.lastWrapDomain = in.GetDomain()
	// "wrap" = prefix marker + the dek (proves the DEK round-trips through the port).
	return &pluginv1.WrapKeyResponse{Wrapped: append([]byte("kms:"), in.GetDek()...), KeyVersion: 7}, nil
}

func (f *fakeClient) UnwrapKey(_ context.Context, in *pluginv1.UnwrapKeyRequest, _ ...grpc.CallOption) (*pluginv1.UnwrapKeyResponse, error) {
	f.lastUnwrapDomain = in.GetDomain()
	w := in.GetWrapped()
	if !bytes.HasPrefix(w, []byte("kms:")) {
		return nil, context.Canceled
	}
	return &pluginv1.UnwrapKeyResponse{Dek: w[len("kms:"):]}, nil
}

// TestPortCustodianRoundTrip proves the port provider: Wrap → WrapKey RPC (self-describing
// envelope), Unwrap → UnwrapKey RPC with the domain threaded through.
func TestPortCustodianRoundTrip(t *testing.T) {
	fc := &fakeClient{}
	c := NewPort(fc, "openbao-transit")
	blob, err := Seal(context.Background(), c, "india", []byte("terraform state"))
	if err != nil {
		t.Fatalf("seal via port: %v", err)
	}
	if fc.lastWrapDomain != "india" {
		t.Fatalf("domain must thread to WrapKey: %q", fc.lastWrapDomain)
	}
	got, enveloped, err := Open(context.Background(), c, blob)
	if err != nil || !enveloped || string(got) != "terraform state" {
		t.Fatalf("port round-trip: %q enveloped=%v err=%v", got, enveloped, err)
	}
	if fc.lastUnwrapDomain != "india" {
		t.Fatalf("domain must thread to UnwrapKey (residency): %q", fc.lastUnwrapDomain)
	}
}

// TestMuxCoexistenceAndMigration is the load-bearing F2 property: a mux whose PRIMARY is
// the port provider (new writes KMS-wrapped) still opens blobs sealed by the LOCAL floor
// (provider-dispatch) — so enabling a KMS never orphans existing/local state, and a
// domain can migrate providers without a flag day (ADR-0100).
func TestMuxCoexistenceAndMigration(t *testing.T) {
	kek := make([]byte, 32)
	_, _ = rand.Read(kek)
	local, _ := NewLocal(kek)
	port := NewPort(&fakeClient{}, "openbao-transit")
	mux := NewMux(port, local) // primary = port; local retained for reads

	// A blob previously sealed by the LOCAL floor...
	localBlob, _ := Seal(context.Background(), local, "default", []byte("old-local-state"))
	// ...opens through the mux (dispatched to the local custodian by its provider tag).
	got, _, err := Open(context.Background(), mux, localBlob)
	if err != nil || string(got) != "old-local-state" {
		t.Fatalf("mux must open a local-wrapped blob: %q %v", got, err)
	}
	// A NEW write through the mux uses the PRIMARY (port/KMS) and also round-trips.
	newBlob, _ := Seal(context.Background(), mux, "default", []byte("new-kms-state"))
	if p, _ := peekProvider(newBlob[len(magic)+uvarintHeaderLen(newBlob):]); p == "local" {
		t.Fatal("new writes through the mux must use the KMS primary, not local")
	}
	got2, _, err := Open(context.Background(), mux, newBlob)
	if err != nil || string(got2) != "new-kms-state" {
		t.Fatalf("mux must open its own KMS-wrapped blob: %q %v", got2, err)
	}
	// An unknown provider fails closed (a KMS not configured/reachable for that domain).
	foreign := makeForeignBlob(t)
	if _, _, err := Open(context.Background(), mux, foreign); err == nil {
		t.Fatal("mux must fail closed on a provider it has no custodian for")
	}
}

// uvarintHeaderLen returns the length of the uvarint(wrappedLen) header so a test can
// locate the wrapped bytes after the magic.
func uvarintHeaderLen(blobAfterMagic []byte) int {
	for i := 0; i < len(blobAfterMagic); i++ {
		if blobAfterMagic[i] < 0x80 {
			return i + 1
		}
	}
	return 1
}

func makeForeignBlob(t *testing.T) []byte {
	t.Helper()
	// A well-formed envelope whose wrapped DEK claims an unconfigured provider.
	wrapped := []byte(`{"p":"aws-kms","d":"us","v":1,"w":"AAAA"}`)
	var b bytes.Buffer
	b.Write(magic)
	b.WriteByte(byte(len(wrapped))) // small len fits one uvarint byte
	b.Write(wrapped)
	b.Write(make([]byte, 40)) // dummy ciphertext
	return b.Bytes()
}
