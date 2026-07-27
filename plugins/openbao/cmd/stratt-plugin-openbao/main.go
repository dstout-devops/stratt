// Command stratt-plugin-openbao serves the cert-issuer Connector plugin over
// the sovereign plugin port (ADR-0046). It is its own binary/build unit: the
// control plane connects to it over gRPC and governs what it may write. It
// advertises both capabilities of the cert-issuer Connector — the cert Syncer
// (Observe) and the issue/renew/revoke multi-op Action (Invoke). The CLM token is
// resolved from the environment at spawn (STRATT_OPENBAO_TOKEN) and never persisted
// (§2.5).
package main

import (
	"os"

	"github.com/dstout-devops/stratt/sdk/pluginserve"

	"github.com/dstout-devops/stratt/plugins/openbao"
)

func main() {
	cfg := openbao.Config{
		PluginID: pluginserve.Env("STRATT_PLUGIN_ID", "openbao"),
		Addr:     pluginserve.Env("STRATT_OPENBAO_ADDR", "http://localhost:8200"),
		Token:    os.Getenv("STRATT_OPENBAO_TOKEN"),
		Mount:    pluginserve.Env("STRATT_OPENBAO_MOUNT", "pki"),
		IntMount: pluginserve.Env("STRATT_OPENBAO_INT_MOUNT", "pki_int"),
		KVMount:  os.Getenv("STRATT_OPENBAO_KV_MOUNT"), // empty ⇒ KV Syncer off (ADR-0099)
	}
	pluginserve.Main(pluginserve.Config{
		Name:   "openbao",
		Server: openbao.NewServer(cfg, pluginserve.Logger()),
		Fields: []any{"openbao_addr", cfg.Addr, "plugin_id", cfg.PluginID},
	})
}
