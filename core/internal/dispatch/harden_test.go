package dispatch

import (
	"context"
	"log/slog"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dstout-devops/stratt/core/internal/actuators"
)

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
	c := pod.Containers[0]
	sc := c.SecurityContext
	if sc == nil {
		t.Fatal("container needs a SecurityContext")
	}
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Fatal("container must run as non-root")
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
