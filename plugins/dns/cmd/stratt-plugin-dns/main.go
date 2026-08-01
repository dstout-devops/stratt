// Command stratt-plugin-dns serves the DNS Connector/Actuator over the sovereign plugin
// port (ADR-0144): it AXFRs the declared zones into the graph, and writes records by
// RFC 2136 dynamic update so a name declared in Git becomes a fact Stratt caused. Its
// own build/CI unit; imports nothing from core/.
package main

import (
	"os"
	"strings"

	"github.com/dstout-devops/stratt/plugins/dns"
	"github.com/dstout-devops/stratt/sdk/pluginserve"
)

func main() {
	cfg := dns.Config{
		PluginID: pluginserve.Env("STRATT_PLUGIN_ID", "dns"),
		Server:   os.Getenv("STRATT_DNS_SERVER"),
		Zones:    fields(os.Getenv("STRATT_DNS_ZONES")),
		// What the names in these zones DENOTE. Declared, never guessed — a projection
		// that correlates onto an existing Entity SETS its kind (see Config.ProjectKind).
		ProjectKind: os.Getenv("STRATT_DNS_PROJECT_KIND"),
		// The TSIG key. Material arrives in the pod's environment from a mounted Secret,
		// the way every other plugin credential does, and never touches a declaration,
		// a log line, or the graph (§2.5). The Syncer has no other channel available:
		// ObserveRequest carries no Envelope, so there are no CredentialRefs on a poll.
		TSIG: dns.TSIGKey{
			Name:      os.Getenv("STRATT_DNS_TSIG_NAME"),
			Secret:    os.Getenv("STRATT_DNS_TSIG_SECRET"),
			Algorithm: os.Getenv("STRATT_DNS_TSIG_ALGORITHM"),
		},
	}
	pluginserve.Main(pluginserve.Config{
		Name:   "dns",
		Server: dns.NewServer(cfg, pluginserve.Logger()),
		Fields: []any{"server", cfg.Server, "zones", cfg.Zones, "plugin_id", cfg.PluginID},
	})
}

// fields splits a comma- or space-separated list, ignoring empties — so
// STRATT_DNS_ZONES="estate.example, dmz.example" behaves the way an operator expects.
func fields(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
