package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/core/internal/bundle"
)

// runBundle handles `stratt bundle …` (ADR-0032): package a directory of Step
// content into a cosign-signable OCI Bundle for pull-mode Sites. Signing itself
// is real cosign — `cosign sign --key`, printed as the next step — so the agent
// verifies exactly what cosign produces, in-process.
//
//	stratt bundle push <content-dir> <ref> --name N --version V --actuator A \
//	    [--command "sh,run.sh"] [--image stratt-ee:dev]
func runBundle(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: stratt bundle push <content-dir> <ref> --name N --version V --actuator A")
	}
	sub := args[0]
	fs := flag.NewFlagSet("bundle "+sub, flag.ExitOnError)
	name := fs.String("name", "", "bundle name (required)")
	version := fs.String("version", "1", "bundle version")
	actuator := fs.String("actuator", "script", "actuator that runs the content (script|ansible|…)")
	command := fs.String("command", "", "comma-separated pod command")
	image := fs.String("image", "", "EE image override (empty = agent default)")
	_ = fs.Parse(args[1:])

	switch sub {
	case "push", "build":
		rest := fs.Args()
		if len(rest) < 2 {
			return fmt.Errorf("usage: stratt bundle %s <content-dir> <ref> --name N", sub)
		}
		if *name == "" {
			return fmt.Errorf("--name is required")
		}
		dir, ref := rest[0], rest[1]
		files, err := readContentDir(dir)
		if err != nil {
			return err
		}
		spec := actuators.JobSpec{Files: files, Image: *image}
		if *command != "" {
			spec.Command = strings.Split(*command, ",")
		}
		cfg, content, err := bundle.Build(*name, *version, *actuator, spec)
		if err != nil {
			return err
		}
		digest, err := bundle.Push(context.Background(), ref, cfg, content)
		if err != nil {
			return err
		}
		fmt.Printf("bundle pushed: %s\n", ref)
		fmt.Printf("manifest digest: %s\n", digest)
		fmt.Printf("next: cosign sign --key cosign.key %s@%s\n", bundleRepo(ref), digest)
		fmt.Printf("then pin %s in the pull-Site's assignment (STRATT_BUNDLE_DIGEST)\n", digest)
		return nil
	default:
		return fmt.Errorf("unknown bundle subcommand %q (push)", sub)
	}
}

// readContentDir reads every regular file under dir into a Files map keyed by
// its path relative to dir (the JobSpec.Files layout the pod mounts).
func readContentDir(dir string) (map[string]string, error) {
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("stratt: read content dir %s: %w", dir, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("stratt: content dir %s is empty", dir)
	}
	return files, nil
}

// bundleRepo strips a :tag so the `cosign sign` hint uses repo@digest.
func bundleRepo(ref string) string {
	slash := strings.LastIndexByte(ref, '/')
	if c := strings.LastIndexByte(ref, ':'); c > slash {
		return ref[:c]
	}
	return ref
}
