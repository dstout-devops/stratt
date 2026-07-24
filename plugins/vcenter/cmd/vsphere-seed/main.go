// Command vsphere-seed shapes a running vcsim into an enterprise topology (ADR-0113 follow-up #3):
// multi-region datacenters, availability-zone clusters, sovereignty tenant folders, and VLAN-tagged
// DVS portgroups — so the vSphere read+build story is demonstrable against realistic enterprise
// scenarios. Dev-only (vcsim is a simulator). Idempotent: re-running creates only what is absent.
//
// Usage: STRATT_VCENTER_URL=https://localhost:8989/sdk go run ./cmd/vsphere-seed
//
//	(or: task dev:vsphere:bootstrap)
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/dstout-devops/stratt/plugins/vcenter"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := vcenter.Config{
		PluginID: "vsphere-seed",
		Endpoint: env("STRATT_VCENTER_URL", "https://localhost:8989/sdk"),
		Username: env("STRATT_VCENTER_USERNAME", "user"),
		Password: env("STRATT_VCENTER_PASSWORD", "pass"),
		Insecure: os.Getenv("STRATT_VCENTER_INSECURE") != "false", // dev/vcsim: insecure by default
	}

	ctx := context.Background()
	client, err := vcenter.Connect(ctx, cfg)
	if err != nil {
		log.Error("connect vcsim", "endpoint", cfg.Endpoint, "error", err)
		os.Exit(1)
	}
	defer client.Logout(ctx) //nolint:errcheck

	created, err := vcenter.Seed(ctx, client.Client, vcenter.DefaultTopology())
	if err != nil {
		log.Error("seed", "created", created, "error", err)
		os.Exit(1)
	}
	if created == 0 {
		log.Info("vsphere estate already seeded (idempotent no-op)", "endpoint", cfg.Endpoint)
	} else {
		log.Info("seeded vsphere enterprise topology", "endpoint", cfg.Endpoint, "objects_created", created)
	}
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
