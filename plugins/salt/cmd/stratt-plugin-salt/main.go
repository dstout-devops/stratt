// Command stratt-plugin-salt serves the Salt plugin over the sovereign plugin
// port (ADR-0046/0047): the grains Syncer (Observe) and the event-bus Emitter
// (Subscribe). Its own binary/build unit; the control plane connects over gRPC
// and governs what it may write and the grant-bound emitter name it publishes as.
package main

import (
	"os"
	"strings"

	"github.com/dstout-devops/stratt/sdk/pluginserve"

	"github.com/dstout-devops/stratt/plugins/salt"
)

func main() {
	cfg := salt.Config{
		PluginID:  pluginserve.Env("STRATT_PLUGIN_ID", "salt"),
		APIURL:    os.Getenv("STRATT_SALT_API_URL"),
		Username:  pluginserve.Env("STRATT_SALT_USERNAME", "stratt"),
		Password:  os.Getenv("STRATT_SALT_PASSWORD"),
		Eauth:     pluginserve.Env("STRATT_SALT_EAUTH", "pam"),
		EventTags: splitTags(os.Getenv("STRATT_SALT_EVENT_TAGS")),
		// ADR-0080 slice 2b: opt into the OS-package inventory collector (a live
		// pkg.list_pkgs round-trip; off by default to keep the cache-only default).
		CollectPackages: os.Getenv("STRATT_SALT_COLLECT_PACKAGES") == "true",
	}
	pluginserve.Main(pluginserve.Config{
		Name:   "salt",
		Server: salt.NewServer(cfg, pluginserve.Logger()),
		Fields: []any{"endpoint", cfg.APIURL, "plugin_id", cfg.PluginID},
	})
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
