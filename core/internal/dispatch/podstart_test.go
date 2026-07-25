package dispatch

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dstout-devops/stratt/types"
)

// These tests exist because an EE image that did not exist produced a Run that
// hung — the dispatcher watched only Pod.Status.Phase, so a pod parked in
// ImagePullBackOff stayed Pending and this loop heartbeated until the Step
// activity's StartToClose ceiling. The abstraction was hiding a failure the
// cluster was reporting in plain text the whole time (§1.8). Nothing below is
// ansible-specific: it is every EE-Job Actuator.

// pending builds a Pending pod whose single container is waiting for `reason`.
func pending(name, reason, message string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "stratt-jobs",
			Labels:    map[string]string{"job-name": "stratt-run-r1-s0"},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "ee",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: message}},
			}},
		},
	}
}

func testDispatcher(t *testing.T, grace time.Duration, objs ...runtime.Object) *Dispatcher {
	t.Helper()
	cs := fake.NewSimpleClientset(objs...)
	// No bus: the diagnosis still has to reach the logs, and publishPreStart must
	// not panic when one is absent.
	return New(Config{Namespace: "stratt-jobs", PodStartGrace: grace}, cs, nil, slog.Default())
}

// TestWaitForPod_FailsOnImagePullBackOff is the regression for the hang. With
// the grace window collapsed, the dispatcher must give up with kubelet's own
// reason rather than waiting out the activity.
func TestWaitForPod_FailsOnImagePullBackOff(t *testing.T) {
	d := testDispatcher(t, time.Millisecond,
		pending("stratt-run-r1-s0-abc", "ImagePullBackOff",
			`Back-off pulling image "stratt-ee-crypto:dev"`))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := d.waitForPod(ctx, "r1", 0, "stratt-run-r1-s0", nil)

	// Before the fix this returned only when ctx expired — the assertion that
	// matters is that it failed on the REASON, not on a timeout.
	if err == nil {
		t.Fatal("expected a failure, got nil: an unpullable image was waited out")
	}
	if ctx.Err() != nil {
		t.Fatal("waitForPod ran until the context expired instead of reporting the reason")
	}
	for _, want := range []string{"ImagePullBackOff", "stratt-ee-crypto:dev", "ee"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("failure message must carry the cluster's own account; missing %q in: %v", want, err)
		}
	}
}

// TestWaitForPod_InvalidImageNameFailsImmediately: a malformed reference can
// never resolve, so it must not serve out the grace window at all.
func TestWaitForPod_InvalidImageNameFailsImmediately(t *testing.T) {
	d := testDispatcher(t, time.Hour, // a grace long enough that serving it would hang the test
		pending("stratt-run-r1-s0-abc", "InvalidImageName", `couldn't parse image name "ee::::"`))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := d.waitForPod(ctx, "r1", 0, "stratt-run-r1-s0", nil); err == nil {
		t.Fatal("expected an immediate failure for a malformed image reference")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("a malformed image reference served the grace window; it can never self-heal")
	}
}

// TestWaitForPod_TransientPullIsWaitedOut is the other half of the trade-off: a
// registry blip must still self-heal, or this fix would turn flaky pulls into
// failed Runs. ErrImagePull is kubelet's FIRST failure; ImagePullBackOff is the
// settled state, and only the grace window separates them.
func TestWaitForPod_TransientPullIsWaitedOut(t *testing.T) {
	pod := pending("stratt-run-r1-s0-abc", "ErrImagePull", "connection reset by peer")
	d := testDispatcher(t, 3*time.Second, pod)

	// The pull succeeds while the dispatcher is still inside its grace.
	go func() {
		time.Sleep(300 * time.Millisecond)
		running := pod.DeepCopy()
		running.Status.Phase = corev1.PodRunning
		running.Status.ContainerStatuses[0].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
		_, _ = d.client.CoreV1().Pods("stratt-jobs").Update(context.Background(), running, metav1.UpdateOptions{})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	name, err := d.waitForPod(ctx, "r1", 0, "stratt-run-r1-s0", nil)
	if err != nil {
		t.Fatalf("a transient pull failure must not fail the Step: %v", err)
	}
	if name != "stratt-run-r1-s0-abc" {
		t.Fatalf("got pod %q", name)
	}
}

// TestClassifyPodBlock covers the narrate/grace/fail asymmetry directly: we
// narrate everything the cluster says and fail only on what we recognize, so an
// unfamiliar reason reaches the operator without inventing a failure.
func TestClassifyPodBlock(t *testing.T) {
	cases := []struct {
		name   string
		pod    corev1.Pod
		want   blockAction
		absent bool
	}{
		{name: "backoff is graced", pod: *pending("p", "ImagePullBackOff", "back-off"), want: blockGrace},
		{name: "bad name is fatal", pod: *pending("p", "InvalidImageName", "nope"), want: blockFatal},
		{name: "missing secret is graced", pod: *pending("p", "CreateContainerConfigError", "secret not found"), want: blockGrace},
		{name: "an unknown reason is narrated, never failed", pod: *pending("p", "SomeFutureKubeletReason", "?"), want: blockNarrate},
		{name: "ContainerCreating is not a block", pod: *pending("p", "ContainerCreating", ""), absent: true},
		{name: "PodInitializing is not a block", pod: *pending("p", "PodInitializing", ""), absent: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, blocked := classifyPodBlock([]corev1.Pod{tc.pod})
			if tc.absent {
				if blocked {
					t.Fatalf("a healthy startup state was reported as a block: %+v", got)
				}
				return
			}
			if !blocked {
				t.Fatal("expected a block")
			}
			if got.action != tc.want {
				t.Fatalf("action = %v, want %v", got.action, tc.want)
			}
		})
	}
}

// TestClassifyPodBlock_Unschedulable: with no container complaining, the pod's
// own PodScheduled condition is the diagnosis — narrated (an autoscaler may
// legitimately take minutes), never failed.
func TestClassifyPodBlock_Unschedulable(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p"},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
				Reason: "Unschedulable", Message: "0/1 nodes are available: insufficient cpu",
			}},
		},
	}
	got, blocked := classifyPodBlock([]corev1.Pod{pod})
	if !blocked {
		t.Fatal("an unschedulable pod must be reported, not silently waited on")
	}
	if got.action != blockNarrate {
		t.Fatalf("unschedulable must be narrated only, got action %v", got.action)
	}
	if !strings.Contains(got.Error(), "insufficient cpu") || !strings.Contains(got.Error(), "scheduling") {
		t.Fatalf("the rendering must carry the scheduler's reason: %q", got.Error())
	}
}

// TestWaitForPod_InitContainerBlockIsSeen: an init container that cannot start
// keeps the pod Pending exactly like the main one, and is just as invisible.
func TestWaitForPod_InitContainerBlockIsSeen(t *testing.T) {
	pod := pending("stratt-run-r1-s0-abc", "ContainerCreating", "")
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
		Name:  "seed",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "no such image"}},
	}}
	d := testDispatcher(t, time.Millisecond, pod)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := d.waitForPod(ctx, "r1", 0, "stratt-run-r1-s0", nil)
	if err == nil || !strings.Contains(err.Error(), "seed") {
		t.Fatalf("an init container's block must be reported by name, got: %v", err)
	}
}

// TestPreStartSeqsCannotCollideWithATool: (RunID, Slice, Seq) is the JetStream
// dedup identity, and a tool's stream counts from 1 — so a pre-start seq sharing
// that space would silently drop either the diagnosis or a real event.
func TestPreStartSeqsCannotCollideWithATool(t *testing.T) {
	if preStartFailSeq >= 0 {
		t.Errorf("preStartFailSeq must be negative, got %d", preStartFailSeq)
	}
	if preStartSeqFloor+preStartNarrationCap > preStartFailSeq {
		t.Errorf("narration (%d..%d) overruns the reserved give-up seq %d",
			preStartSeqFloor, preStartSeqFloor+preStartNarrationCap-1, preStartFailSeq)
	}
}

// TestPreStartEventIsRunScoped: a pod that never started is a fact about the RUN — there are no
// tasks for it to be about (ADR-0121 D4). The spine stamps its own run-level events so a descent
// surface pins them by the same content-blind rule it pins a plugin's, rather than needing a case
// per spine kind.
//
// Asserted on the constructed event rather than through the bus, because every dispatcher in this
// file is built without one; before the construction was split out, this stamping had nowhere to be
// verified and would have been another mechanism nothing exercised.
func TestPreStartEventIsRunScoped(t *testing.T) {
	ev := preStartRunEvent("r1", 0, -2, "pod-start-blocked", types.RunEventError, "site-a", podBlock{
		pod: "stratt-run-r1-s0-abc", reason: "ImagePullBackOff", container: "ee",
		message: `Back-off pulling image "stratt-ee-crypto:dev"`,
	})
	if ev.Scope != types.RunEventScopeRun {
		t.Errorf("a pod that never started describes the Run, not a task; got scope %q", ev.Scope)
	}
	if ev.Level != types.RunEventError || ev.Kind != "pod-start-blocked" {
		t.Errorf("level/kind must survive the split: %q %q", ev.Level, ev.Kind)
	}
	if ev.Payload["reason"] != "ImagePullBackOff" {
		t.Errorf("the cluster's own reason must reach the stream: %#v", ev.Payload)
	}
}
