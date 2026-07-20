// Command stratt-plugin-salt serves the Salt plugin over the sovereign plugin
// port (ADR-0046/0047): the grains Syncer (Observe) and the event-bus Emitter
// (Subscribe). Its own binary/build unit; the control plane connects over gRPC
// and governs what it may write and the grant-bound emitter name it publishes as.
package main

import (
	"log/slog"
	"net"
	"os"
	"strings"

	"google.golang.org/grpc"

	"github.com/dstout-devops/stratt/plugins/salt"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := salt.Config{
		PluginID:  env("STRATT_PLUGIN_ID", "salt"),
		APIURL:    os.Getenv("STRATT_SALT_API_URL"),
		Username:  env("STRATT_SALT_USERNAME", "stratt"),
		Password:  os.Getenv("STRATT_SALT_PASSWORD"),
		Eauth:     env("STRATT_SALT_EAUTH", "pam"),
		EventTags: splitTags(os.Getenv("STRATT_SALT_EVENT_TAGS")),
		// ADR-0080 slice 2b: opt into the OS-package inventory collector (a live
		// pkg.list_pkgs round-trip; off by default to keep the cache-only default).
		CollectPackages: os.Getenv("STRATT_SALT_COLLECT_PACKAGES") == "true",
	}
	addr := env("STRATT_PLUGIN_LISTEN", ":9090")

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("listen", "addr", addr, "error", err)
		os.Exit(1)
	}
	srv := grpc.NewServer()
	pluginv1.RegisterPluginServiceServer(srv, salt.NewServer(cfg, log))
	log.Info("salt plugin serving", "addr", addr, "endpoint", cfg.APIURL, "plugin_id", cfg.PluginID)
	if err := srv.Serve(lis); err != nil {
		log.Error("serve", "error", err)
		os.Exit(1)
	}
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// splitTags parses a comma-separated tag-prefix allowlist (empty = forward all).
func splitTags(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
