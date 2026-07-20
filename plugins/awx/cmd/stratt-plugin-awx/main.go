// Command stratt-plugin-awx serves the AWX/AAP Connector over the sovereign plugin
// port (ADR-0046/0047): it OBSERVEs an AWX Controller's /api/v2 and projects its
// automation estate — job templates, workflows, schedules, organizations, teams — as
// `ansible.*` ObservedEntities. Read-only (§1.2): AWX stays authoritative and keeps
// executing; the control plane connects over gRPC and governs what it may write. Its
// own binary/build/CI unit (module isolation, ADR-0046).
package main

import (
	"log/slog"
	"net"
	"os"

	"google.golang.org/grpc"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/dstout-devops/stratt/plugins/awx"
	"github.com/dstout-devops/stratt/sdk/secretbroker"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	endpoint := os.Getenv("STRATT_AWX_ENDPOINT")
	if endpoint == "" {
		log.Error("STRATT_AWX_ENDPOINT is required (the AWX Controller base URL, e.g. https://awx.example.com)")
		os.Exit(1)
	}
	client := awx.New(awx.Config{
		Endpoint:     endpoint,
		Token:        os.Getenv("STRATT_AWX_TOKEN"),
		ControllerID: os.Getenv("STRATT_AWX_CONTROLLER_ID"), // "" ⇒ the endpoint host
	})
	cfg := awx.ServerConfig{
		PluginID:           env("STRATT_PLUGIN_ID", "awx"),
		AllowEmptyFullSync: os.Getenv("STRATT_AWX_ALLOW_EMPTY_FULL_SYNC") == "true",
	}

	// The SecretBroker backs the adopt/materialize Action ONLY (§2.5): it resolves the AWX
	// CredentialRef in-pod under this plugin's confined RBAC. Best-effort — a Syncer-only
	// deployment (or local dev) with no in-cluster config runs fine with broker=nil; the
	// Action then fails closed. The Syncer path never touches it.
	var broker *secretbroker.Resolver
	if rc, err := rest.InClusterConfig(); err == nil {
		if cs, cerr := kubernetes.NewForConfig(rc); cerr == nil {
			broker = secretbroker.New(cs, env("STRATT_SECRET_NAMESPACE", os.Getenv("POD_NAMESPACE")))
			log.Info("adopt SecretBroker ready", "secretNamespace", env("STRATT_SECRET_NAMESPACE", os.Getenv("POD_NAMESPACE")))
		} else {
			log.Warn("k8s client for SecretBroker unavailable; adopt/materialize will fail closed", "error", cerr)
		}
	} else {
		log.Info("no in-cluster config; adopt/materialize disabled (Syncer-only)", "reason", err)
	}

	addr := env("STRATT_PLUGIN_LISTEN", ":9090")
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("listen", "addr", addr, "error", err)
		os.Exit(1)
	}
	srv := grpc.NewServer()
	pluginv1.RegisterPluginServiceServer(srv, awx.NewServer(cfg, client, broker, log))
	log.Info("awx Connector serving", "addr", addr, "endpoint", endpoint, "plugin_id", cfg.PluginID)
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
