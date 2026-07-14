package leader

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"
)

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestLeaderAcquiresAndReleases proves the guard wiring against a fake API
// server: a lone replica acquires the Lease and onLead is invoked with a live
// leader-scoped context; on shutdown that context is cancelled (controllers
// stop) and Run returns.
func TestLeaderAcquiresAndReleases(t *testing.T) {
	client := fake.NewSimpleClientset()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	led := make(chan context.Context, 1)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, client, Config{
			Identity: "pod-1", Namespace: "test",
			LeaseDuration: time.Second, RenewDeadline: 600 * time.Millisecond, RetryPeriod: 200 * time.Millisecond,
		}, testLog(), func(leaderCtx context.Context) { led <- leaderCtx })
	}()

	var leaderCtx context.Context
	select {
	case leaderCtx = <-led:
	case <-time.After(5 * time.Second):
		t.Fatal("never acquired leadership")
	}
	if leaderCtx.Err() != nil {
		t.Fatal("leader-scoped context should be live while leading")
	}

	// Graceful shutdown cancels the leader context (controllers stop) and Run exits.
	cancel()
	select {
	case <-leaderCtx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("leader context not cancelled after shutdown")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run should return context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}

// TestLeaderConfigValidation proves the identity/namespace guards.
func TestLeaderConfigValidation(t *testing.T) {
	client := fake.NewSimpleClientset()
	ctx := context.Background()
	if err := Run(ctx, client, Config{Namespace: "test"}, testLog(), func(context.Context) {}); err == nil {
		t.Fatal("missing identity must error")
	}
	if err := Run(ctx, client, Config{Identity: "pod-1"}, testLog(), func(context.Context) {}); err == nil {
		t.Fatal("missing namespace must error")
	}
}
