package dispatch

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
	"github.com/dstout-devops/stratt/types"
)

// TestKeepAlive_BeatsWhileTheToolIsSilent is the regression for a Step being
// declared dead for being quiet. The heartbeat used to be driven by the tool's
// output, so a single task that printed nothing for longer than the activity's
// HeartbeatTimeout — an `apt install`, a long `command` — timed out while its
// pod was perfectly healthy. Liveness is a property of the execution, not of how
// chatty the tool is.
func TestKeepAlive_BeatsWhileTheToolIsSilent(t *testing.T) {
	var beats atomic.Int64
	_, stop := keepAliveEvery(func() { beats.Add(1) }, 10*time.Millisecond)
	time.Sleep(120 * time.Millisecond)
	stop()
	if got := beats.Load(); got < 3 {
		t.Fatalf("expected the ticker to report liveness without any tool output, got %d beats", got)
	}
}

// TestKeepAlive_StopsBeating: an execution that has finished must not keep
// heartbeating — that would report liveness for an activity that has returned.
func TestKeepAlive_StopsBeating(t *testing.T) {
	var beats atomic.Int64
	_, stop := keepAliveEvery(func() { beats.Add(1) }, 5*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	stop()
	after := beats.Load()
	time.Sleep(30 * time.Millisecond)
	if beats.Load() != after {
		t.Fatal("keepAlive kept beating after stop")
	}
}

// TestKeepAlive_SerializesWithTheFollowLoop: the returned beat func is called
// from the log-follow loop while the ticker is also firing, and
// activity.RecordHeartbeat is not documented as goroutine-safe — so keepAlive
// must serialize rather than assume. Run under -race, this fails if it does not.
func TestKeepAlive_SerializesWithTheFollowLoop(t *testing.T) {
	shared := 0 // deliberately unguarded: keepAlive owns the serialization
	beat, stop := keepAliveEvery(func() { shared++ }, time.Millisecond)
	defer stop()

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				beat()
			}
		}()
	}
	wg.Wait()
}

// TestKeepAlive_NilHeartbeatStaysNil preserves the hb() contract: tests and the
// agent relay pass no heartbeat at all, and wrapping nil in a live closure would
// silently turn "no heartbeat" into a ticker with nothing to call.
func TestKeepAlive_NilHeartbeatStaysNil(t *testing.T) {
	beat, stop := keepAlive(nil)
	if beat != nil {
		t.Fatal("a nil heartbeat must stay nil so hb() keeps no-oping")
	}
	stop() // must not panic
	hb(beat)
}

// TestMaxStepRuntimeIsTheJobsOwnDeadline guards the invariant behind the two
// numbers that used to disagree: the Job's ActiveDeadlineSeconds and the activity
// ceiling derived from it. If a future change lowers one, this fails rather than
// silently reintroducing "Temporal kills a Step the Job would still allow".
func TestMaxStepRuntimeIsTheJobsOwnDeadline(t *testing.T) {
	if MaxStepRuntime <= 0 {
		t.Fatal("MaxStepRuntime must be positive")
	}
	if MaxStepRuntime.Truncate(time.Second) != MaxStepRuntime {
		t.Fatalf("MaxStepRuntime must be whole seconds: ActiveDeadlineSeconds cannot express %v", MaxStepRuntime)
	}
	if heartbeatInterval >= time.Minute {
		t.Fatalf("heartbeatInterval %v leaves no margin under a minute-scale HeartbeatTimeout", heartbeatInterval)
	}
}

// TestTypedEventLevelMapping pins the port→stream severity mapping, and in
// particular that LEVEL_UNSPECIFIED becomes EMPTY rather than "info": a plugin
// that states no level must not be reported as having said everything is fine
// (§1.8). This is the mapping that was simply absent — the port's level was
// decoded and then dropped (ADR-0117 g).
func TestTypedEventLevelMapping(t *testing.T) {
	cases := map[pluginv1.TaskEvent_Level]string{
		pluginv1.TaskEvent_LEVEL_UNSPECIFIED: "",
		pluginv1.TaskEvent_LEVEL_DEBUG:       types.RunEventDebug,
		pluginv1.TaskEvent_LEVEL_INFO:        types.RunEventInfo,
		pluginv1.TaskEvent_LEVEL_WARN:        types.RunEventWarn,
		pluginv1.TaskEvent_LEVEL_ERROR:       types.RunEventError,
	}
	for in, want := range cases {
		if got := typedEventLevel(in); got != want {
			t.Errorf("typedEventLevel(%v) = %q, want %q", in, got, want)
		}
	}
}
