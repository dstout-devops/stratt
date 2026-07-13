package bundle

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"

	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sigstore/sigstore/pkg/cryptoutils"
	sig "github.com/sigstore/sigstore/pkg/signature"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"

	"github.com/dstout-devops/stratt/core/internal/actuators"
)

// signCosignStyle pushes a cosign-format key-based signature artifact
// (sha256-<hex>.sig) for manifestDigest into dst, signed with priv — exactly the
// shape `cosign sign --key` produces, so verifyIn exercises the real path.
func signCosignStyle(t *testing.T, ctx context.Context, dst oras.Target, manifestDigest string, priv *ecdsa.PrivateKey) {
	t.Helper()
	payload := []byte(`{"critical":{"identity":{"docker-reference":"stratt/bundle"},"image":{"docker-manifest-digest":"` + manifestDigest + `"},"type":"cosign container image signature"},"optional":null}`)
	signer, err := sig.LoadSigner(priv, crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := signer.SignMessage(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString(raw)

	// The signature image: an empty config + one layer = the payload, with the
	// signature in the layer annotation (cosign's storage).
	cfgBlob := []byte("{}")
	cfgDesc := content.NewDescriptorFromBytes("application/vnd.oci.image.config.v1+json", cfgBlob)
	if err := dst.Push(ctx, cfgDesc, bytes.NewReader(cfgBlob)); err != nil {
		t.Fatal(err)
	}
	layerDesc := content.NewDescriptorFromBytes("application/vnd.dev.cosign.simplesigning.v1+json", payload)
	layerDesc.Annotations = map[string]string{cosignSigAnnotation: b64}
	if err := dst.Push(ctx, layerDesc, bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	man := ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    cfgDesc,
		Layers:    []ocispec.Descriptor{layerDesc},
	}
	manBytes, _ := json.Marshal(man)
	manDesc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageManifest, manBytes)
	if err := dst.Push(ctx, manDesc, bytes.NewReader(manBytes)); err != nil {
		t.Fatal(err)
	}
	d, _ := godigest.Parse(manifestDigest)
	if err := dst.Tag(ctx, manDesc, "sha256-"+d.Encoded()+".sig"); err != nil {
		t.Fatal(err)
	}
}

// TestVerifyCosignStyle proves the verify-before-execute gate in-process against
// a real cosign-format signature: a valid signature passes; a wrong pinned
// digest, a wrong key, and an unsigned Bundle are all HARD-refused (§1.8/§1.5).
func TestVerifyCosignStyle(t *testing.T) {
	ctx := context.Background()
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	pubPEM, err := cryptoutils.MarshalPublicKeyToPEM(priv.Public())
	if err != nil {
		t.Fatal(err)
	}

	store := memory.New()
	spec := actuators.JobSpec{Files: map[string]string{"project/run.sh": "echo hi"}, Command: []string{"sh", "run.sh"}}
	cfg, contentLayer, err := Build("patch", "1", "script", spec)
	if err != nil {
		t.Fatal(err)
	}
	manDesc, err := packInto(ctx, store, "1", cfg, contentLayer)
	if err != nil {
		t.Fatal(err)
	}
	digest := manDesc.Digest.String()
	signCosignStyle(t, ctx, store, digest, priv)

	p, err := pullFrom(ctx, store, "1", "example.com/stratt/patch")
	if err != nil {
		t.Fatal(err)
	}
	if p.ManifestDigest != digest {
		t.Fatalf("pulled digest %s != %s", p.ManifestDigest, digest)
	}

	// Happy path: valid signature + matching pinned digest → verified, spec out.
	gotSpec, actuator, err := verifiedSpecIn(ctx, store, p, pubPEM, digest)
	if err != nil {
		t.Fatalf("valid bundle must verify: %v", err)
	}
	if actuator != "script" || gotSpec.Files["project/run.sh"] != "echo hi" {
		t.Fatalf("reconstructed spec wrong: %q %+v", actuator, gotSpec)
	}

	// Wrong pinned digest → refuse (the Assignment integrity anchor).
	if err := verifyIn(ctx, store, p, pubPEM, "sha256:0000000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Fatal("wrong pinned digest must be refused")
	}

	// Wrong key → refuse (authenticity).
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	otherPEM, _ := cryptoutils.MarshalPublicKeyToPEM(other.Public())
	if err := verifyIn(ctx, store, p, otherPEM, digest); err == nil {
		t.Fatal("signature by a different key must be refused")
	}

	// Unsigned Bundle → refuse (no .sig artifact present).
	bare := memory.New()
	if _, err := packInto(ctx, bare, "1", cfg, contentLayer); err != nil {
		t.Fatal(err)
	}
	pb, _ := pullFrom(ctx, bare, "1", "example.com/stratt/patch")
	if err := verifyIn(ctx, bare, pb, pubPEM, ""); err == nil {
		t.Fatal("an unsigned Bundle must be refused")
	}

	// Tampered content layer (digest mismatch) → refuse even with a valid sig.
	tampered := p
	tampered.Content = append([]byte(nil), p.Content...)
	tampered.Content[len(tampered.Content)-1] ^= 0xff
	if _, _, err := verifiedSpecIn(ctx, store, tampered, pubPEM, digest); err == nil {
		t.Fatal("tampered content must be refused")
	}
}
