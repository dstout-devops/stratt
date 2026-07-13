package sitegw

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/dstout-devops/stratt/core/internal/actuators"
	"github.com/dstout-devops/stratt/core/internal/dispatch"
	"github.com/dstout-devops/stratt/core/internal/siteproto"
)

// dial connects to the dev NATS, or skips (mirrors TestOpenFGAAgreement's
// skip-when-substrate-down posture — this exercises the REAL transport).
func dial(t *testing.T) *Gateway {
	t.Helper()
	url := os.Getenv("STRATT_NATS_URL")
	if url == "" {
		url = "nats://localhost:4222"
	}
	gw, err := Connect(url, "sitegw-test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Skipf("no NATS at %s: %v", url, err)
	}
	return gw
}

// uniq is a per-run id so MsgID dedup never collides with a prior test run
// (FileStorage streams persist across runs). Dot-free: a Site name is a single
// NATS subject token (ADR-0032).
func uniq() string { return "test-" + strconv.FormatInt(time.Now().UnixNano(), 36) }

// TestGatewayDispatchRoundTrip proves the hub↔Site NATS path end to end against
// a real server: Dispatch → the Site consumes → PublishResult → AwaitResult.
func TestGatewayDispatchRoundTrip(t *testing.T) {
	gw := dial(t)
	defer gw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := gw.EnsureStreams(ctx); err != nil {
		t.Fatalf("ensure streams: %v", err)
	}

	site := uniq()
	runID := uniq()

	// The Site side: consume one dispatch, echo a successful result.
	consumeCtx, stopConsume := context.WithCancel(ctx)
	defer stopConsume()
	go func() {
		_ = gw.ConsumeDispatch(consumeCtx, site, func(c context.Context, req siteproto.DispatchRequest) error {
			return gw.PublishResult(c, siteproto.DispatchResult{
				RunID: req.RunID, Slice: req.Slice, Site: req.Site,
				Result: dispatch.Result{Succeeded: true,
					PerTarget:    map[string]string{"t1": actuators.StatusOK},
					SiteByTarget: map[string]string{"t1": req.Site}},
			})
		})
	}()
	time.Sleep(300 * time.Millisecond) // let the durable consumer register

	res, err := gw.DispatchAndAwait(ctx, siteproto.DispatchRequest{
		RunID: runID, Slice: 0, Site: site, Actuator: "script",
		Spec: actuators.JobSpec{Command: []string{"true"}},
	}, nil)
	if err != nil {
		t.Fatalf("dispatch/await: %v", err)
	}
	if !res.Succeeded || res.SiteByTarget["t1"] != site {
		t.Fatalf("round-trip result wrong: %+v", res)
	}
}

// TestGatewayRefusesEnvMaterial proves the §2.5 backstop: a JobSpec carrying
// plain Env is refused at the dispatch door — no material reaches the wire.
func TestGatewayRefusesEnvMaterial(t *testing.T) {
	gw := dial(t)
	defer gw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = gw.EnsureStreams(ctx)

	err := gw.Dispatch(ctx, siteproto.DispatchRequest{
		RunID: uniq(), Slice: 0, Site: uniq(),
		Spec: actuators.JobSpec{Env: map[string]string{"TF_HTTP_PASSWORD": "secret"}},
	})
	if err == nil {
		t.Fatal("dispatch with Env material must be refused (§2.5)")
	}
}

// TestGatewayLiveness proves the KV heartbeat + read path.
func TestGatewayLiveness(t *testing.T) {
	gw := dial(t)
	defer gw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := gw.EnsureStreams(ctx); err != nil {
		t.Fatalf("ensure streams: %v", err)
	}
	site := uniq()
	if err := gw.Heartbeat(ctx, siteproto.Liveness{Site: site, Mode: "push", At: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	live, err := gw.LiveSites(ctx)
	if err != nil {
		t.Fatalf("live sites: %v", err)
	}
	if _, ok := live[site]; !ok {
		t.Fatalf("heartbeated site %s not reported live: %v", site, live)
	}
}

// TestGatewayCancel proves the ephemeral cancel signal reaches a subscriber.
func TestGatewayCancel(t *testing.T) {
	gw := dial(t)
	defer gw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	site := uniq()
	got := make(chan string, 1)
	unsub, err := gw.SubscribeCancel(site, func(runID string) { got <- runID })
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsub()

	if err := gw.Cancel(ctx, site, "run-xyz"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	select {
	case runID := <-got:
		if runID != "run-xyz" {
			t.Fatalf("cancel payload: %q", runID)
		}
	case <-ctx.Done():
		t.Fatal("cancel signal never arrived")
	}
}
