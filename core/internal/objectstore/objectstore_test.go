package objectstore

import "testing"

// TestConfigFromEnvPrecedence pins the ONE-place resolution order: canonical
// STRATT_OBJECTSTORE_* wins, with STRATT_EVIDENCE_* then STRATT_AWS_* as
// backward-compatible fallbacks (so the historical shared-endpoint deployments keep
// working while a dedicated var severs the coupling).
func TestConfigFromEnvPrecedence(t *testing.T) {
	// Clear everything, then assert the fallback ladder rung by rung.
	for _, k := range []string{"STRATT_OBJECTSTORE_ENDPOINT", "STRATT_EVIDENCE_ENDPOINT", "STRATT_AWS_ENDPOINT",
		"STRATT_OBJECTSTORE_REGION", "STRATT_EVIDENCE_REGION", "STRATT_AWS_REGION", "STRATT_OBJECTSTORE_PATH_STYLE"} {
		t.Setenv(k, "")
	}

	// Defaults: no endpoint (SDK default resolver), us-east-1, path-style on.
	if c := ConfigFromEnv(); c.Endpoint != "" || c.Region != "us-east-1" || !c.PathStyle {
		t.Fatalf("defaults wrong: %+v", c)
	}

	// AWS fallback (the historical shared endpoint) is honored.
	t.Setenv("STRATT_AWS_ENDPOINT", "http://aws-mock:9000")
	t.Setenv("STRATT_AWS_REGION", "eu-west-1")
	if c := ConfigFromEnv(); c.Endpoint != "http://aws-mock:9000" || c.Region != "eu-west-1" {
		t.Fatalf("AWS fallback not honored: %+v", c)
	}

	// EVIDENCE overrides AWS.
	t.Setenv("STRATT_EVIDENCE_ENDPOINT", "http://evidence:9000")
	if c := ConfigFromEnv(); c.Endpoint != "http://evidence:9000" {
		t.Fatalf("evidence should override aws: %+v", c)
	}

	// Canonical OBJECTSTORE wins over both.
	t.Setenv("STRATT_OBJECTSTORE_ENDPOINT", "http://objectstore:9000")
	t.Setenv("STRATT_OBJECTSTORE_REGION", "us-west-2")
	if c := ConfigFromEnv(); c.Endpoint != "http://objectstore:9000" || c.Region != "us-west-2" {
		t.Fatalf("canonical objectstore must win: %+v", c)
	}

	// PathStyle is on unless explicitly disabled for virtual-host AWS.
	t.Setenv("STRATT_OBJECTSTORE_PATH_STYLE", "false")
	if ConfigFromEnv().PathStyle {
		t.Fatal("STRATT_OBJECTSTORE_PATH_STYLE=false must disable path-style")
	}
}
