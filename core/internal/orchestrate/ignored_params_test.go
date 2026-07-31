package orchestrate

import (
	"bytes"
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"
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
