package dispatch

import (
	"context"
	"log/slog"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/types"
)

// TestCreateJob_RejectsVaultMount proves the pod/EE-Job path refuses a backend: vault
// CredentialRef LOUDLY (ADR-0094 §1.8) — the kubelet cannot inject Vault material, so a
// vault mount must never degrade to an empty secretKeyRef. Vault is plugin-path only.
func TestCreateJob_RejectsVaultMount(t *testing.T) {
	cs := fake.NewSimpleClientset()
	d := New(Config{Namespace: "stratt-jobs", EEImage: "ee:test"}, cs, nil, slog.Default())

	err := d.createJob(context.Background(), "stratt-run-r0-s0", "r0",
		actuators.JobSpec{Command: []string{"echo", "hi"}},
		[]CredentialMount{{RefName: "cred/vaulted", Vault: &types.VaultLocator{Mount: "secret", Path: "x"}}})
	if err == nil {
		t.Fatal("a vault-backed CredentialRef must fail closed on the pod path, not inject empty")
	}
	if _, getErr := cs.BatchV1().Jobs("stratt-jobs").Get(context.Background(), "stratt-run-r0-s0", metav1.GetOptions{}); getErr == nil {
		t.Fatal("no Job must be created when a vault mount is refused")
	}
}

// The ephemeral EE Job — the one pod that runs arbitrary tool content — must be
// sandboxed (§7.1/§3, enterprise-readiness hardening): non-root, no privilege
// escalation, all caps dropped, no SA token, resource-capped, deadline-bounded.
func TestCreateJob_Sandboxed(t *testing.T) {
	cs := fake.NewSimpleClientset()
	d := New(Config{Namespace: "stratt-jobs", EEImage: "ee:test"}, cs, nil, slog.Default())

	if err := d.createJob(context.Background(), "stratt-run-r0-s0", "r0",
		actuators.JobSpec{Command: []string{"echo", "hi"}}, nil); err != nil {
		t.Fatal(err)
	}
	job, err := cs.BatchV1().Jobs("stratt-jobs").Get(context.Background(), "stratt-run-r0-s0", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod := job.Spec.Template.Spec

	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Fatal("the arbitrary-content pod must NOT mount a service-account token")
	}
	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds <= 0 {
		t.Fatal("a hung Job must be deadline-bounded")
	}
	if pod.SecurityContext == nil || pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot {
		t.Fatal("pod must be enforced non-root")
	}
	// RunAsNonRoot is only SATISFIABLE with a numeric uid: the EE image's USER is a
	// name (`runner`), which the kubelet rejects as "cannot verify non-root" before
	// start. The pod spec must pin the numeric uid the EE image is built with.
	if pod.SecurityContext.RunAsUser == nil || *pod.SecurityContext.RunAsUser == 0 {
		t.Fatal("pod must pin a numeric non-zero RunAsUser (named image USER is unverifiable under RunAsNonRoot)")
	}
	c := pod.Containers[0]
	sc := c.SecurityContext
	if sc == nil {
		t.Fatal("container needs a SecurityContext")
	}
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Fatal("container must run as non-root")
	}
	if sc.RunAsUser == nil || *sc.RunAsUser == 0 {
		t.Fatal("container must pin a numeric non-zero RunAsUser")
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Fatal("privilege escalation must be off")
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Drop) == 0 || sc.Capabilities.Drop[0] != "ALL" {
		t.Fatal("all capabilities must be dropped")
	}
	if sc.SeccompProfile == nil {
		t.Fatal("seccomp profile must be set")
	}
	if c.Resources.Limits.Cpu().IsZero() || c.Resources.Limits.Memory().IsZero() {
		t.Fatal("a runaway must be CPU/memory-capped")
	}
}
