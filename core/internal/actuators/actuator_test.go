package actuators

import (
	"strings"
	"testing"
)

// TestJobSpecRemoteSafe covers the §2.5 gate (ADR-0032): a JobSpec bound for a
// remote Site or a Bundle must carry no plain Env, since Env may hold
// credential material (the opentofu TF_HTTP_PASSWORD).
func TestJobSpecRemoteSafe(t *testing.T) {
	// Empty Env is remote-safe (the common ansible/script inline case).
	if err := (JobSpec{Command: []string{"true"}}).RemoteSafe(); err != nil {
		t.Fatalf("empty-Env spec must be remote-safe: %v", err)
	}
	if err := (JobSpec{Env: map[string]string{}}).RemoteSafe(); err != nil {
		t.Fatalf("zero-len Env must be remote-safe: %v", err)
	}

	// Any non-empty Env is refused — even a non-secret key, conservatively.
	err := (JobSpec{Env: map[string]string{"TF_HTTP_PASSWORD": "s3cr3t", "TF_DATA_DIR": "/x"}}).RemoteSafe()
	if err == nil {
		t.Fatal("a spec with Env must NOT be remote-safe")
	}
	// The error names the keys (sorted) but must never leak a value (§2.5).
	if !strings.Contains(err.Error(), "TF_DATA_DIR") || !strings.Contains(err.Error(), "TF_HTTP_PASSWORD") {
		t.Fatalf("error must name the offending keys: %v", err)
	}
	if strings.Contains(err.Error(), "s3cr3t") {
		t.Fatalf("error must NOT contain the material value: %v", err)
	}
	// Keys must be sorted for a deterministic message.
	if strings.Index(err.Error(), "TF_DATA_DIR") > strings.Index(err.Error(), "TF_HTTP_PASSWORD") {
		t.Fatalf("keys must be sorted: %v", err)
	}
}
