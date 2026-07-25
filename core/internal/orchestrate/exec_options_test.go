package orchestrate

import (
	"testing"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/dstout-devops/stratt/core/internal/dispatch"
)

// TestExecActivityOptionsOutliveTheJob is the regression for a Step that could
// never succeed if it took more than ten minutes. The dispatcher puts
// dispatch.MaxStepRuntime (6h) on the Job's own ActiveDeadlineSeconds, while the
// activity that waits for that Job carried an independent 10m StartToCloseTimeout
// — so Temporal killed and retried any longer Step, three times, into a timeout
// whose message said nothing about what had happened. Nothing in repo caught it
// because every play we run finishes in seconds.
func TestExecActivityOptionsOutliveTheJob(t *testing.T) {
	base := workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute,
		HeartbeatTimeout:    time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	}
	got := execActivityOptions(base)

	if got.StartToCloseTimeout <= dispatch.MaxStepRuntime {
		t.Fatalf("execution ceiling %v does not outlive the Job's own deadline %v: "+
			"Temporal would kill a Step the Job still permits",
			got.StartToCloseTimeout, dispatch.MaxStepRuntime)
	}
	if got.HeartbeatTimeout <= 0 {
		t.Fatal("HeartbeatTimeout must stay set: it is the real liveness check for a long execution")
	}
	// The base options are right for the bookkeeping activities around execution,
	// and must not be widened along with it — a long ceiling on a control-plane
	// call turns a wedged call into a silent stall.
	if base.StartToCloseTimeout != 10*time.Minute {
		t.Fatal("execActivityOptions mutated its input; the bookkeeping activities share it")
	}
	if got.RetryPolicy != base.RetryPolicy {
		t.Fatal("retry policy should carry through unchanged")
	}
}
