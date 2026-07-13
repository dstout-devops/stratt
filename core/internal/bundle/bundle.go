// Package bundle builds and verifies Bundles (charter §2.3, ADR-0032): a
// cosign-signed OCI artifact packaging a prepared, credential-free Step for a
// pull-mode Site. A Bundle carries the JobSpec.Files tree + the command/image
// and actuator name — everything dispatch.Dispatcher.Run needs EXCEPT
// credentials (pointers resolved Site-local at spawn) and EXCEPT any plain
// JobSpec.Env material (refused at build by RemoteSafe — §2.5).
//
// This file is the dependency-free core: the manifest config shape and the
// content layer (a deterministic tar+gz of the Files tree). OCI push/pull and
// cosign verification live alongside it and are the only parts that need the
// oras / sigstore dependencies.
package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/dstout-devops/stratt/core/internal/actuators"
)

// OCI media types for a Stratt Bundle (charter §1.5: our own artifact, OCI is
// the transport beneath it).
const (
	ConfigMediaType  = "application/vnd.stratt.bundle.config.v1+json"
	ContentMediaType = "application/vnd.stratt.bundle.content.v1.tar+gzip"
	ArtifactType     = "application/vnd.stratt.bundle.v1"
)

// Config is the Bundle's manifest config blob — the metadata a pull-agent needs
// to reconstruct the JobSpec. It holds NO credential material and NO plain Env
// (build refuses a non-RemoteSafe spec). contentDigest pins the content layer so
// verification checks integrity independent of the registry.
type Config struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// Actuator names the in-agent Interpreter that runs this content.
	Actuator string `json:"actuator"`
	// Command/Image mirror the prepared JobSpec (Image "" ⇒ the agent default).
	Command []string `json:"command,omitempty"`
	Image   string   `json:"image,omitempty"`
	// ContentDigest is sha256:<hex> of the tar+gz content layer.
	ContentDigest string `json:"contentDigest"`
}

// Build packs a prepared, credential-free JobSpec into a Bundle Config plus its
// content layer bytes. It REFUSES a spec carrying plain Env material (§2.5): a
// signed, distributable artifact must never bake a secret.
func Build(name, version, actuator string, spec actuators.JobSpec) (Config, []byte, error) {
	if err := spec.RemoteSafe(); err != nil {
		return Config{}, nil, fmt.Errorf("bundle %s: %w", name, err)
	}
	content, err := packFiles(spec.Files)
	if err != nil {
		return Config{}, nil, err
	}
	sum := sha256.Sum256(content)
	return Config{
		Name: name, Version: version, Actuator: actuator,
		Command: spec.Command, Image: spec.Image,
		ContentDigest: "sha256:" + hex.EncodeToString(sum[:]),
	}, content, nil
}

// JobSpec reconstructs the pod content from a verified Config + content layer.
// The caller MUST have verified the signature and that sha256(content) matches
// cfg.ContentDigest before calling this (see verify.go).
func (cfg Config) JobSpec(content []byte) (actuators.JobSpec, error) {
	files, err := unpackFiles(content)
	if err != nil {
		return actuators.JobSpec{}, err
	}
	return actuators.JobSpec{Files: files, Command: cfg.Command, Image: cfg.Image}, nil
}

// ContentMatches reports whether raw content hashes to the pinned digest.
func (cfg Config) ContentMatches(content []byte) bool {
	sum := sha256.Sum256(content)
	return cfg.ContentDigest == "sha256:"+hex.EncodeToString(sum[:])
}

// Marshal renders the Config blob canonically (stable key order via json).
func (cfg Config) Marshal() ([]byte, error) { return json.Marshal(cfg) }

// packFiles writes the Files map into a deterministic tar+gz (sorted keys, fixed
// mode/time) so an identical Files tree always yields an identical digest — the
// property the pinned contentDigest relies on.
func packFiles(files map[string]string) ([]byte, error) {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(gz)
	for _, k := range keys {
		body := []byte(files[k])
		hdr := &tar.Header{Name: k, Mode: 0o444, Size: int64(len(body))}
		// Deterministic: no ModTime (zero value), no uid/gid, no names.
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, fmt.Errorf("bundle: tar header %s: %w", k, err)
		}
		if _, err := tw.Write(body); err != nil {
			return nil, fmt.Errorf("bundle: tar write %s: %w", k, err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func unpackFiles(content []byte) (map[string]string, error) {
	gz, err := gzip.NewReader(bytes.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("bundle: gunzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	files := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		var b bytes.Buffer
		if _, err := b.ReadFrom(tr); err != nil {
			return nil, fmt.Errorf("bundle: tar read %s: %w", hdr.Name, err)
		}
		files[hdr.Name] = b.String()
	}
	return files, nil
}
