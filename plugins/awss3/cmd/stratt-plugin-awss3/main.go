// Command stratt-plugin-awss3 serves the AWS S3 Connector plugin over the sovereign
// plugin port (ADR-0046/0097): a metadata-only bucket Syncer + bucket lifecycle Actions.
// The control plane dials it and governs what it may write.
package main

import (
	"os"
	"strings"

	"github.com/dstout-devops/stratt/sdk/pluginserve"

	"github.com/dstout-devops/stratt/plugins/awss3"
)

func main() {
	cfg := awss3.Config{
		PluginID:  pluginserve.Env("STRATT_PLUGIN_ID", "awss3"),
		Endpoint:  os.Getenv("STRATT_AWSS3_ENDPOINT"),
		Region:    pluginserve.Env("STRATT_AWSS3_REGION", "us-east-1"),
		PathStyle: pluginserve.Env("STRATT_AWSS3_PATH_STYLE", "true") == "true", // SeaweedFS + most S3-compatibles need path-style
		// Destructive Actions refuse these (ADR-0097): an operator list PLUS the
		// Evidence WORM bucket (ADR-0029), so awss3 can't be the hole in write-once.
		ProtectedBuckets: protectedBuckets(),
		// statestore capability (ADR-0105): set STRATT_AWSS3_STATE_BUCKET to make this plugin a
		// statestore provider; STRATT_AWSS3_STATE_CREDENTIAL_REF names the §2.5 CredentialRef.
		StateBucket:        os.Getenv("STRATT_AWSS3_STATE_BUCKET"),
		StateCredentialRef: os.Getenv("STRATT_AWSS3_STATE_CREDENTIAL_REF"),
	}
	pluginserve.Main(pluginserve.Config{
		Name:   "awss3",
		Server: awss3.NewServer(cfg, pluginserve.Logger()),
		Fields: []any{"region", cfg.Region, "plugin_id", cfg.PluginID, "protected", len(cfg.ProtectedBuckets)},
	})
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
