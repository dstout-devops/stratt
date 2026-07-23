// Command stratt-plugin-awss3 serves the AWS S3 Connector plugin over the sovereign
// plugin port (ADR-0046/0097): a metadata-only bucket Syncer + bucket lifecycle Actions.
// The control plane dials it and governs what it may write.
package main

import (
	"log/slog"
	"net"
	"os"
	"strings"

	"google.golang.org/grpc"

	"github.com/dstout-devops/stratt/plugins/awss3"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := awss3.Config{
		PluginID:  env("STRATT_PLUGIN_ID", "awss3"),
		Endpoint:  os.Getenv("STRATT_AWSS3_ENDPOINT"),
		Region:    env("STRATT_AWSS3_REGION", "us-east-1"),
		PathStyle: env("STRATT_AWSS3_PATH_STYLE", "true") == "true", // SeaweedFS + most S3-compatibles need path-style
		// Destructive Actions refuse these (ADR-0097): an operator list PLUS the
		// Evidence WORM bucket (ADR-0029), so awss3 can't be the hole in write-once.
		ProtectedBuckets: protectedBuckets(),
		// statestore capability (ADR-0105): set STRATT_AWSS3_STATE_BUCKET to make this plugin a
		// statestore provider; STRATT_AWSS3_STATE_CREDENTIAL_REF names the §2.5 CredentialRef.
		StateBucket:        os.Getenv("STRATT_AWSS3_STATE_BUCKET"),
		StateCredentialRef: os.Getenv("STRATT_AWSS3_STATE_CREDENTIAL_REF"),
	}
	addr := env("STRATT_PLUGIN_LISTEN", ":9090")

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("listen", "addr", addr, "error", err)
		os.Exit(1)
	}
	srv := grpc.NewServer()
	pluginv1.RegisterPluginServiceServer(srv, awss3.NewServer(cfg, log))
	log.Info("awss3 plugin serving", "addr", addr, "region", cfg.Region, "plugin_id", cfg.PluginID, "protected", len(cfg.ProtectedBuckets))
	if err := srv.Serve(lis); err != nil {
		log.Error("serve", "error", err)
		os.Exit(1)
	}
}

// protectedBuckets is STRATT_AWSS3_PROTECTED_BUCKETS (comma-separated) plus the Evidence
// bucket name if configured (STRATT_EVIDENCE_BUCKET), deduplicated.
func protectedBuckets() []string {
	seen := map[string]bool{}
	var out []string
	add := func(b string) {
		b = strings.TrimSpace(b)
		if b != "" && !seen[b] {
			seen[b] = true
			out = append(out, b)
		}
	}
	for _, b := range strings.Split(os.Getenv("STRATT_AWSS3_PROTECTED_BUCKETS"), ",") {
		add(b)
	}
	add(os.Getenv("STRATT_EVIDENCE_BUCKET"))
	return out
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
