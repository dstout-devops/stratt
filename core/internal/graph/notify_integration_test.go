package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func TestNotifySinkCRUD(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	sink := types.Sink{
		Name: "ops-webhook", Kind: types.SinkWebhook, CredentialRef: "ops-hook-cred",
		Config: types.SinkConfig{Method: "POST", BodyTemplate: `{"text":"{{.subject}}"}`},
	}
	if err := store.UpsertNotifySink(ctx, sink); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := store.GetNotifySink(ctx, "ops-webhook")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.CredentialRef != "ops-hook-cred" || got.Config.BodyTemplate != `{"text":"{{.subject}}"}` {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	// Upsert is idempotent + updates.
	sink.Config.Method = "PUT"
	if err := store.UpsertNotifySink(ctx, sink); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if got, _ = store.GetNotifySink(ctx, "ops-webhook"); got.Config.Method != "PUT" {
		t.Fatalf("update not applied: %+v", got)
	}

	list, err := store.ListNotifySinks(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %+v", err, list)
	}
	if err := store.DeleteNotifySink(ctx, "ops-webhook"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.GetNotifySink(ctx, "ops-webhook"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound after delete, got %v", err)
	}
}

func TestSubscriptionCRUD(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	sub := types.Subscription{
		Name: "crit-drift", On: []string{types.NoticeFindingOpen},
		Match: `event.payload.severity == "critical"`, Sink: "ops-webhook", CooldownSeconds: 60,
	}
	if err := store.UpsertSubscription(ctx, sub); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := store.GetSubscription(ctx, "crit-drift")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Sink != "ops-webhook" || got.CooldownSeconds != 60 || len(got.On) != 1 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if err := store.DeleteSubscription(ctx, "crit-drift"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.GetSubscription(ctx, "crit-drift"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestRecordAndListDeliveries(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if err := store.RecordDelivery(ctx, types.NotifyDelivery{
		NoticeKind: types.NoticeRunFailed, Subject: "run-1", Subscription: "s", Sink: "k",
		Status: types.DeliveryDelivered,
	}); err != nil {
		t.Fatalf("record delivered: %v", err)
	}
	if err := store.RecordDelivery(ctx, types.NotifyDelivery{
		NoticeKind: types.NoticeRunFailed, Subject: "run-2", Subscription: "s", Sink: "k",
		Status: types.DeliveryFailed, Detail: "endpoint rejected the request",
	}); err != nil {
		t.Fatalf("record failed: %v", err)
	}
	list, err := store.ListDeliveries(ctx, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 deliveries, got %d", len(list))
	}
	// A failure is visible on the status surface with its detail (§1.8).
	var sawFailure bool
	for _, d := range list {
		if d.Status == types.DeliveryFailed && d.Detail == "endpoint rejected the request" {
			sawFailure = true
		}
	}
	if !sawFailure {
		t.Fatalf("failed delivery detail not surfaced: %+v", list)
	}
}
