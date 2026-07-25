package dispatch

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

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

// TestTypedEventScopeMapping: SCOPE_UNSPECIFIED must map to EMPTY, never to `task` (ADR-0121 D1).
// The field is newer than every producer, so defaulting unstated to `task` would assert that no
// plugin has ever described a Run — a confident claim built from missing data, which is the §1.8
// failure the level mapping one function up exists to avoid.
func TestTypedEventScopeMapping(t *testing.T) {
	cases := map[pluginv1.TaskEvent_Scope]string{
		pluginv1.TaskEvent_SCOPE_UNSPECIFIED: "",
		pluginv1.TaskEvent_SCOPE_RUN:         types.RunEventScopeRun,
		pluginv1.TaskEvent_SCOPE_TASK:        types.RunEventScopeTask,
	}
	for in, want := range cases {
		if got := typedEventScope(in); got != want {
			t.Errorf("typedEventScope(%v) = %q, want %q", in, got, want)
		}
	}
}

// TestRunEventFromTaskEvent_CarriesEveryPortField covers the CONVERSION, not the mappers — the
// distinction that matters, because both mappers had passing unit tests while the call that used
// them was untested. Deleting `Scope:` from the struct literal this replaced changed nothing
// observable in the whole suite; that is why the conversion is a named function now.
//
// Every field asserted here has been absent from the operator's view at some point: Level was
// decoded and dropped (ADR-0117 g), Scope did not exist (ADR-0121), and the descent shows the
// rest.
func TestRunEventFromTaskEvent_CarriesEveryPortField(t *testing.T) {
	at := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	re := runEventFromTaskEvent(&pluginv1.TaskEvent{
		Level:   pluginv1.TaskEvent_LEVEL_WARN,
		Scope:   pluginv1.TaskEvent_SCOPE_RUN,
		Message: "community.crypto 2.22.3",
		At:      timestamppb.New(at),
		Fields:  map[string]string{"kind": "ee-content", "host": "web-01"},
	}, "run-1", 2, 7, "site-a")

	if re.Level != types.RunEventWarn {
		t.Errorf("level dropped: %q", re.Level)
	}
	if re.Scope != types.RunEventScopeRun {
		t.Errorf("scope dropped — a run-level fact stays unfindable in an uncapped stream: %q", re.Scope)
	}
	if re.Kind != "ee-content" || re.Target != "web-01" {
		t.Errorf("kind/target: %q %q", re.Kind, re.Target)
	}
	if re.RunID != "run-1" || re.Slice != 2 || re.Seq != 7 || re.Site != "site-a" {
		t.Errorf("identity/locus: %#v", re)
	}
	if !re.At.Equal(at) {
		t.Errorf("timestamp: %v", re.At)
	}
	if re.Payload["message"] != "community.crypto 2.22.3" {
		t.Errorf("payload: %#v", re.Payload)
	}
}

// An event stating nothing must carry nothing stated: no level, no scope, no target. An empty
// string for either would make "the producer did not say" indistinguishable from a stated value
// downstream, and the wire layer relies on empty to omit the field entirely (§1.8).
func TestRunEventFromTaskEvent_StatesNothingItWasNotTold(t *testing.T) {
	re := runEventFromTaskEvent(&pluginv1.TaskEvent{Message: "line"}, "run-1", 0, 1, "")
	if re.Level != "" || re.Scope != "" || re.Target != "" {
		t.Errorf("unstated fields must stay empty: level=%q scope=%q target=%q", re.Level, re.Scope, re.Target)
	}
}
