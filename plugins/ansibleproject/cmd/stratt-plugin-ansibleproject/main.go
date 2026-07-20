// Command stratt-plugin-ansibleproject serves the ansible-project Connector over the
// sovereign plugin port (ADR-0046/0047): it OBSERVEs a raw Ansible content root (a Git
// checkout / mounted directory of playbooks, roles, requirements.yml, inventory) and
// projects its artifacts as read-only `ansible.*` ObservedEntities. This is "Ansible
// without AWX" — the primitive half of the `ansible` domain the AWX Connector's
// orchestration half feeds into. Read-only (§1.2, §2.5): Git stays authoritative; the
// plugin holds no write credential to the root. Its own binary/build/CI unit (module
// isolation, ADR-0046).
package main

import (
	"log/slog"
	"net"
	"os"

	"google.golang.org/grpc"

	"github.com/dstout-devops/stratt/plugins/ansibleproject"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	root := os.Getenv("STRATT_ANSIBLE_PROJECT_ROOT")
	if root == "" {
		log.Error("STRATT_ANSIBLE_PROJECT_ROOT is required (the Ansible content root — a mounted Git checkout / directory)")
		os.Exit(1)
	}
	client := ansibleproject.New(ansibleproject.Config{
		Root:      root,
		ProjectID: os.Getenv("STRATT_ANSIBLE_PROJECT_ID"), // "" ⇒ base name of the root
	})
	cfg := ansibleproject.ServerConfig{
		PluginID:           env("STRATT_PLUGIN_ID", "ansibleproject"),
		AllowEmptyFullSync: os.Getenv("STRATT_ANSIBLE_PROJECT_ALLOW_EMPTY_FULL_SYNC") == "true",
	}

	addr := env("STRATT_PLUGIN_LISTEN", ":9090")
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("listen", "addr", addr, "error", err)
		os.Exit(1)
	}
	srv := grpc.NewServer()
	pluginv1.RegisterPluginServiceServer(srv, ansibleproject.NewServer(cfg, client, log))
	log.Info("ansible-project Connector serving", "addr", addr, "root", root, "plugin_id", cfg.PluginID)
	if err := srv.Serve(lis); err != nil {
		log.Error("serve", "error", err)
		os.Exit(1)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
