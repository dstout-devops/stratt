package bundle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry/remote"
)

// fixedCreated makes the packed manifest reproducible — the same Bundle content
// must yield the same manifest digest so the pinned digest is stable (ADR-0032).
const fixedCreated = "1970-01-01T00:00:00Z"

// plainHTTP reports whether a ref points at a local/dev registry that speaks
// plain HTTP (localhost / 127.0.0.1 / a host:port with no TLS). Production refs
// use HTTPS. STRATT_BUNDLE_INSECURE=true forces plain HTTP for a dev registry
// reached by a non-loopback address (e.g. the kind host gateway).
func plainHTTP(ref string) bool {
	if os.Getenv("STRATT_BUNDLE_INSECURE") == "true" {
		return true
	}
	host := ref
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	return strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.0.0.1") ||
		strings.HasPrefix(host, "registry:") || strings.HasPrefix(host, "zot:")
}

// Push packs a Config + content layer as an OCI artifact and pushes it to ref
// (e.g. "localhost:5555/stratt/bundles/patch-web:1"). Returns the manifest
// digest — the value an Assignment pins and cosign signs.
func Push(ctx context.Context, ref string, cfg Config, contentLayer []byte) (string, error) {
	store := memory.New()
	tag := refTag(ref)
	if _, err := packInto(ctx, store, tag, cfg, contentLayer); err != nil {
		return "", err
	}
	repo, err := remote.NewRepository(ref)
	if err != nil {
		return "", fmt.Errorf("bundle: repo %s: %w", ref, err)
	}
	repo.PlainHTTP = plainHTTP(ref)
	desc, err := oras.Copy(ctx, store, tag, repo, tag, oras.DefaultCopyOptions)
	if err != nil {
		return "", fmt.Errorf("bundle: push to %s: %w", ref, err)
	}
	return desc.Digest.String(), nil
}

// packInto pushes the config + content blobs and packs+tags the Bundle manifest
// into dst (a memory store, then Copy-ed to a remote repo by Push; a store
// directly in tests). Returns the manifest descriptor.
func packInto(ctx context.Context, dst oras.Target, ref string, cfg Config, contentLayer []byte) (ocispec.Descriptor, error) {
	cfgBytes, err := cfg.Marshal()
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	contentDesc := content.NewDescriptorFromBytes(ContentMediaType, contentLayer)
	if err := dst.Push(ctx, contentDesc, bytes.NewReader(contentLayer)); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("bundle: push content: %w", err)
	}
	cfgDesc := content.NewDescriptorFromBytes(ConfigMediaType, cfgBytes)
	if err := dst.Push(ctx, cfgDesc, bytes.NewReader(cfgBytes)); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("bundle: push config: %w", err)
	}
	manifestDesc, err := oras.PackManifest(ctx, dst, oras.PackManifestVersion1_1, ArtifactType,
		oras.PackManifestOptions{
			Layers:              []ocispec.Descriptor{contentDesc},
			ConfigDescriptor:    &cfgDesc,
			ManifestAnnotations: map[string]string{ocispec.AnnotationCreated: fixedCreated},
		})
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("bundle: pack manifest: %w", err)
	}
	if err := dst.Tag(ctx, manifestDesc, ref); err != nil {
		return ocispec.Descriptor{}, err
	}
	return manifestDesc, nil
}

// Pulled is a fetched Bundle: its parsed Config, its content layer bytes, and
// the manifest digest the caller must check against the pinned digest and the
// signature payload BEFORE trusting any of it (verify.go).
type Pulled struct {
	Config         Config
	Content        []byte
	ManifestDigest string
	// Repository is the ref with the tag/digest stripped — where the cosign
	// signature artifact lives (sha256-<hex>.sig in the same repo).
	Repository string
}

// Pull fetches the artifact at ref. It performs NO verification — the caller
// MUST Verify before reconstructing/executing the JobSpec.
func Pull(ctx context.Context, ref string) (Pulled, error) {
	repo, err := remote.NewRepository(ref)
	if err != nil {
		return Pulled{}, fmt.Errorf("bundle: repo %s: %w", ref, err)
	}
	repo.PlainHTTP = plainHTTP(ref)
	return pullFrom(ctx, repo, refTag(ref), refRepository(ref))
}

// pullFrom is the transport-agnostic pull core (a remote repo in production, a
// memory store in tests) — resolve the manifest, fetch config + content layer.
func pullFrom(ctx context.Context, src oras.ReadOnlyTarget, ref, repository string) (Pulled, error) {
	manifestDesc, err := src.Resolve(ctx, ref)
	if err != nil {
		return Pulled{}, fmt.Errorf("bundle: resolve %s: %w", ref, err)
	}
	manifestBytes, err := content.FetchAll(ctx, src, manifestDesc)
	if err != nil {
		return Pulled{}, err
	}
	var man ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &man); err != nil {
		return Pulled{}, fmt.Errorf("bundle: decode manifest: %w", err)
	}
	cfgBytes, err := content.FetchAll(ctx, src, man.Config)
	if err != nil {
		return Pulled{}, err
	}
	var cfg Config
	if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
		return Pulled{}, fmt.Errorf("bundle: decode config: %w", err)
	}
	if len(man.Layers) == 0 {
		return Pulled{}, fmt.Errorf("bundle: manifest has no content layer")
	}
	contentLayer, err := content.FetchAll(ctx, src, man.Layers[0])
	if err != nil {
		return Pulled{}, err
	}
	return Pulled{
		Config: cfg, Content: contentLayer,
		ManifestDigest: manifestDesc.Digest.String(),
		Repository:     repository,
	}, nil
}

// refTag returns the tag or digest portion of a ref (default "latest").
func refTag(ref string) string {
	repo := refRepository(ref)
	rest := strings.TrimPrefix(ref, repo)
	switch {
	case strings.HasPrefix(rest, "@"):
		return strings.TrimPrefix(rest, "@")
	case strings.HasPrefix(rest, ":"):
		return strings.TrimPrefix(rest, ":")
	default:
		return "latest"
	}
}

// refRepository strips the :tag or @digest, leaving host/path.
func refRepository(ref string) string {
	// A digest ref: host/path@sha256:...
	if i := strings.LastIndexByte(ref, '@'); i >= 0 {
		return ref[:i]
	}
	// A tag ref: host/path:tag — but the host may carry a :port, so only treat
	// the LAST colon after the last slash as the tag separator.
	slash := strings.LastIndexByte(ref, '/')
	if c := strings.LastIndexByte(ref, ':'); c > slash {
		return ref[:c]
	}
	return ref
}
