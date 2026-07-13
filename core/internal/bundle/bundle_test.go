package bundle

import (
	"reflect"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/actuators"
)

func TestBuildRoundTripAndDeterminism(t *testing.T) {
	spec := actuators.JobSpec{
		Files:   map[string]string{"project/site.yml": "- hosts: all", "inventory/hosts": "localhost"},
		Command: []string{"ansible-runner", "run", "/runner"},
		Image:   "stratt-ee:dev",
	}
	cfg, content, err := Build("patch-web", "1", "ansible", spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if cfg.Name != "patch-web" || cfg.Actuator != "ansible" || cfg.Image != "stratt-ee:dev" {
		t.Fatalf("config fields wrong: %+v", cfg)
	}
	if !cfg.ContentMatches(content) {
		t.Fatal("content must match its pinned digest")
	}
	// Tamper one byte → digest mismatch (the tamper-refusal primitive, §1.8/§1.5).
	bad := append([]byte(nil), content...)
	bad[len(bad)-1] ^= 0xff
	if cfg.ContentMatches(bad) {
		t.Fatal("tampered content must NOT match the pinned digest")
	}
	// Reconstruct the JobSpec — Files round-trip exactly.
	got, err := cfg.JobSpec(content)
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}
	if !reflect.DeepEqual(got.Files, spec.Files) {
		t.Fatalf("files round-trip: got %v want %v", got.Files, spec.Files)
	}
	if !reflect.DeepEqual(got.Command, spec.Command) || got.Image != spec.Image {
		t.Fatalf("command/image round-trip: %+v", got)
	}

	// Deterministic: identical Files ⇒ identical digest (the pin's foundation).
	cfg2, _, err := Build("patch-web", "1", "ansible", spec)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ContentDigest != cfg2.ContentDigest {
		t.Fatalf("digest must be deterministic: %s vs %s", cfg.ContentDigest, cfg2.ContentDigest)
	}
}

func TestBuildRefusesEnvMaterial(t *testing.T) {
	// §2.5: a Bundle is a distributable artifact — it must never bake material.
	_, _, err := Build("x", "1", "opentofu", actuators.JobSpec{
		Env: map[string]string{"TF_HTTP_PASSWORD": "secret"},
	})
	if err == nil {
		t.Fatal("Build must refuse a spec carrying Env material (§2.5)")
	}
}
