package orchestrate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func TestIgnoredParams(t *testing.T) {
	cases := []struct {
		name     string
		args     string
		consumed []string
		want     []string
	}{
		{
			// THE INVERSION, and the whole reason the port field is `consumed` rather than
			// `ignored`. A provider that declares nothing does not look perfect — it has
			// everything it was sent reported, which is the honest reading of "it told us
			// nothing about what it read".
			name: "a silent provider has everything reported",
			args: `{"name":"web-01","params":{"ami":"x","instanceType":"y","region":"z"}}`,
			want: []string{"ami", "instanceType", "region"},
		}, {
			name:     "a provider that consumed everything is silent",
			args:     `{"name":"web-01","params":{"region":"us-east-1"}}`,
			consumed: []string{"region"},
		}, {
			name:     "only the difference is reported",
			args:     `{"params":{"region":"us-east-1","ami":"x"}}`,
			consumed: []string{"region"},
			want:     []string{"ami"},
		}, {
			// No params sent means no difference to state. Reporting on every build would train
			// an operator to ignore the channel, which costs more than it buys.
			name:     "no params sent, nothing to say",
			args:     `{"name":"web-01","placement":{}}`,
			consumed: nil,
		}, {
			name:     "an empty params object is the same as none",
			args:     `{"name":"web-01","params":{}}`,
			consumed: nil,
		}, {
			// The SHARED launch interface is out of scope. `placement` is the instructive case:
			// kubecompute accepts and ignores it BY DESIGN (a pod is placed by the scheduler) and
			// ADR-0123 D2 settled that it is accepted rather than refused so the interface stays
			// shared. Folding it in would report a designed no-op as a defect on every build.
			name:     "the shared launch interface is not opaque params",
			args:     `{"name":"web-01","labels":{"fleet":"web"},"placement":{"subnet":"app"}}`,
			consumed: nil,
		}, {
			// A provider naming a key it was never sent is not an error here — the interesting
			// direction is the other one, and inventing a second diagnosis would compete with the
			// contract validation that already governs the request.
			name:     "a consumed key that was never sent is ignored",
			args:     `{"params":{"region":"us-east-1"}}`,
			consumed: []string{"region", "somethingElse"},
		}, {
			// Unparseable args already failed the Action with a better message; a second, worse
			// one competing with it helps nobody.
			name:     "unparseable args stay quiet",
			args:     `{{{`,
			consumed: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ignoredParams([]byte(c.args), c.consumed)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("ignoredParams = %v, want %v", got, c.want)
			}
		})
	}
}

// The output has to be stable: a Run event whose payload reorders between otherwise identical runs
// is unreadable, and worse, looks like a change.
func TestIgnoredParamsIsOrdered(t *testing.T) {
	args := []byte(`{"params":{"zeta":1,"alpha":2,"mu":3}}`)
	for i := 0; i < 20; i++ {
		got := ignoredParams(args, nil)
		want := []string{"alpha", "mu", "zeta"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d: %v, want %v", i, got, want)
		}
	}
}

// The REPORT itself, exercised through the function the Action path calls.
//
// HONEST LIMIT, stated because the gap matters: Activities.Bus is a concrete *events.Bus rather
// than an interface, so the RunEvent publish cannot be faked without a live NATS. What this covers
// is that surfaceIgnoredParams is reached, decides correctly, and names the params an operator
// needs — the log half of the same report. The publish half follows the identical shape as
// surfaceRejections beside it, and the remaining unverified link is one `res.GetConsumedParams()`
// passthrough sitting among identical lines in InvokeRaw.
func TestSurfaceIgnoredParamsReportsAndStaysQuiet(t *testing.T) {
	var buf bytes.Buffer
	a := &Activities{Log: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))}

	a.surfaceIgnoredParams(context.Background(), "run-1", "kubecompute/create-host", nil)
	if buf.Len() != 0 {
		t.Fatalf("nothing ignored must say nothing — a warning on every build trains an operator to "+
			"ignore the channel: %q", buf.String())
	}

	a.surfaceIgnoredParams(context.Background(), "run-1", "kubecompute/create-host", []string{"ami", "region"})
	out := buf.String()
	for _, want := range []string{"ignored", "ami", "region", "kubecompute/create-host", "run-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report omits %q — an operator needs the params, the action and the run: %q", want, out)
		}
	}
}

// fakeBus captures what orchestration publishes, so the half of surfaceIgnoredParams that an
// operator actually reads can be asserted without a NATS (the EventBus port, ADR-0151 D4 follow-up).
type fakeBus struct {
	events   []types.RunEvent
	failNext error
}

func (f *fakeBus) Publish(_ context.Context, ev types.RunEvent) error {
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return err
	}
	f.events = append(f.events, ev)
	return nil
}
func (f *fakeBus) PublishNotice(context.Context, types.Notice) error { return nil }
func (f *fakeBus) Tail(context.Context, string, func(types.RunEvent) error) error {
	return nil
}

// THE BOOKED GAP, closed. The log half was tested; the PUBLISH half was not, because the field was
// a concrete *events.Bus and a unit test has no bus to give it. The publish half is the one that
// matters: a warning that reaches only the daemon log is a warning nobody sees.
func TestSurfaceIgnoredParamsPublishesTheRunEvent(t *testing.T) {
	bus := &fakeBus{}
	a := &Activities{Bus: bus, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	a.surfaceIgnoredParams(context.Background(), "run-1", "kubecompute/create-host", nil)
	if len(bus.events) != 0 {
		t.Fatalf("nothing ignored must publish nothing: %+v", bus.events)
	}

	a.surfaceIgnoredParams(context.Background(), "run-1", "kubecompute/create-host", []string{"ami", "region"})
	if len(bus.events) != 1 {
		t.Fatalf("one report, one event: %+v", bus.events)
	}
	ev := bus.events[0]
	if ev.RunID != "run-1" || ev.Kind != "params-ignored" {
		t.Errorf("the event must be addressable to the Run and identifiable by kind: %+v", ev)
	}
	// WARN, not INFO: this is a declaration that will silently stop being honoured if a binding
	// changes, and an INFO event is one nobody filters for.
	if ev.Level != types.RunEventWarn {
		t.Errorf("level: got %v, want warn", ev.Level)
	}
	if ev.Scope != types.RunEventScopeRun {
		t.Errorf("scope: got %v, want run — it is a property of the Run, not of one target", ev.Scope)
	}
	if got, _ := ev.Payload["action"].(string); got != "kubecompute/create-host" {
		t.Errorf("payload must name the action that ignored them: %+v", ev.Payload)
	}
	params, _ := ev.Payload["params"].([]string)
	if len(params) != 2 || params[0] != "ami" || params[1] != "region" {
		t.Errorf("payload must carry the params, in the deterministic order the sort guarantees: %+v", ev.Payload)
	}
	// The reason is load-bearing, not decoration: the reader's first question is whether they broke
	// something, and usually they have not.
	if reason, _ := ev.Payload["reason"].(string); !strings.Contains(reason, "did not read these declared params") {
		t.Errorf("payload must say WHY it is being reported: %+v", ev.Payload)
	}
}

// A bus that refuses must not take the build down with it. Losing the trace is bad; losing a build
// because the trace could not be written is worse — and the failure still has to reach the log.
func TestSurfaceIgnoredParamsSurvivesABusThatRefuses(t *testing.T) {
	var buf bytes.Buffer
	bus := &fakeBus{failNext: errors.New("nats is down")}
	a := &Activities{Bus: bus, Log: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))}

	a.surfaceIgnoredParams(context.Background(), "run-1", "kubecompute/create-host", []string{"ami"})

	if !strings.Contains(buf.String(), "nats is down") {
		t.Errorf("a failed publish must be logged, or the trace is lost twice over: %q", buf.String())
	}
}
