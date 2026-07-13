package bundle

import (
	"bytes"
	"context"
	"crypto"
	"encoding/base64"
	"encoding/json"
	"fmt"

	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sigstore/sigstore/pkg/cryptoutils"
	sig "github.com/sigstore/sigstore/pkg/signature"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"

	"github.com/dstout-devops/stratt/core/internal/actuators"
)

// cosign key-based signature storage: `cosign sign --key` pushes a companion
// artifact tagged sha256-<hex>.sig in the same repo, whose layer BLOB is the
// simple-signing payload and whose layer ANNOTATION carries the base64 raw
// signature over that payload. We reproduce cosign's verification exactly,
// in-process — no cosign CLI, no exec surface (ADR-0032; dependency-scout).
const (
	cosignSigAnnotation = "dev.cosignproject.cosign/signature"
)

// simpleSigning is the subset of cosign's payload we check: the digest the
// signature binds. If this doesn't equal the pulled manifest digest, the
// signature is for a DIFFERENT artifact and must not be trusted.
type simpleSigning struct {
	Critical struct {
		Image struct {
			DockerManifestDigest string `json:"docker-manifest-digest"`
		} `json:"image"`
	} `json:"critical"`
}

// Verify enforces the verify-before-execute gate (§1.8, §1.5): the pulled
// Bundle's manifest digest must (a) equal the Assignment's pinned digest and
// (b) carry a cosign signature over that exact digest by the pinned public key.
// Returns nil ONLY if both hold; any tamper, wrong key, missing signature, or
// digest mismatch is a hard refusal — the agent never unpacks a bad Bundle.
func Verify(ctx context.Context, p Pulled, pubPEM []byte, pinnedDigest string) error {
	repo, err := remote.NewRepository(p.Repository + ":latest")
	if err != nil {
		return fmt.Errorf("bundle: signature repo: %w", err)
	}
	repo.PlainHTTP = plainHTTP(p.Repository)
	return verifyIn(ctx, repo, p, pubPEM, pinnedDigest)
}

// verifyIn is the transport-agnostic verification core (a remote repo in
// production, a memory store in tests). It fetches the cosign signature
// artifact (sha256-<hex>.sig) from src and reproduces cosign's key-based check.
func verifyIn(ctx context.Context, src oras.ReadOnlyTarget, p Pulled, pubPEM []byte, pinnedDigest string) error {
	if pinnedDigest != "" && p.ManifestDigest != pinnedDigest {
		return fmt.Errorf("bundle: manifest digest %s != pinned %s (tampered or wrong artifact)", p.ManifestDigest, pinnedDigest)
	}
	pub, err := cryptoutils.UnmarshalPEMToPublicKey(pubPEM)
	if err != nil {
		return fmt.Errorf("bundle: parse public key: %w", err)
	}
	verifier, err := sig.LoadVerifier(pub, crypto.SHA256)
	if err != nil {
		return fmt.Errorf("bundle: load verifier: %w", err)
	}

	d, err := godigest.Parse(p.ManifestDigest)
	if err != nil {
		return fmt.Errorf("bundle: bad manifest digest: %w", err)
	}
	sigTag := "sha256-" + d.Encoded() + ".sig"
	sigDesc, err := src.Resolve(ctx, sigTag)
	if err != nil {
		return fmt.Errorf("bundle: no cosign signature for %s (unsigned or tampered): %w", p.ManifestDigest, err)
	}
	mBytes, err := content.FetchAll(ctx, src, sigDesc)
	if err != nil {
		return err
	}
	var man ocispec.Manifest
	if err := json.Unmarshal(mBytes, &man); err != nil {
		return fmt.Errorf("bundle: decode signature manifest: %w", err)
	}

	var lastErr error = fmt.Errorf("no cosign signature layer found")
	for _, layer := range man.Layers {
		b64, ok := layer.Annotations[cosignSigAnnotation]
		if !ok {
			continue
		}
		sigRaw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			lastErr = err
			continue
		}
		payload, err := content.FetchAll(ctx, src, layer)
		if err != nil {
			lastErr = err
			continue
		}
		if err := verifier.VerifySignature(bytes.NewReader(sigRaw), bytes.NewReader(payload)); err != nil {
			lastErr = fmt.Errorf("signature does not verify against the pinned key: %w", err)
			continue
		}
		// The signature is authentic — now confirm it binds THIS manifest, not a
		// replayed signature over a different artifact.
		var ss simpleSigning
		if err := json.Unmarshal(payload, &ss); err != nil {
			lastErr = err
			continue
		}
		if ss.Critical.Image.DockerManifestDigest != p.ManifestDigest {
			lastErr = fmt.Errorf("signature binds %s, not %s", ss.Critical.Image.DockerManifestDigest, p.ManifestDigest)
			continue
		}
		return nil // authentic + bound to this digest
	}
	return fmt.Errorf("bundle: signature verification failed for %s: %w", p.ManifestDigest, lastErr)
}

// VerifiedSpec verifies a pulled Bundle and, only if it passes, checks the
// content digest and reconstructs the JobSpec ready for dispatch. The returned
// actuator name selects the agent's Interpreter.
func VerifiedSpec(ctx context.Context, p Pulled, pubPEM []byte, pinnedDigest string) (actuators.JobSpec, string, error) {
	repo, err := remote.NewRepository(p.Repository + ":latest")
	if err != nil {
		return actuators.JobSpec{}, "", fmt.Errorf("bundle: signature repo: %w", err)
	}
	repo.PlainHTTP = plainHTTP(p.Repository)
	return verifiedSpecIn(ctx, repo, p, pubPEM, pinnedDigest)
}

func verifiedSpecIn(ctx context.Context, src oras.ReadOnlyTarget, p Pulled, pubPEM []byte, pinnedDigest string) (actuators.JobSpec, string, error) {
	if err := verifyIn(ctx, src, p, pubPEM, pinnedDigest); err != nil {
		return actuators.JobSpec{}, "", err
	}
	if !p.Config.ContentMatches(p.Content) {
		return actuators.JobSpec{}, "", fmt.Errorf("bundle: content layer does not match its pinned digest (§1.5)")
	}
	spec, err := p.Config.JobSpec(p.Content)
	return spec, p.Config.Actuator, err
}
