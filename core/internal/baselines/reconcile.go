// Package baselines reconciles declared Baselines (charter §2.4, ADR-0019)
// onto Temporal Schedules — "Baseline cadences" are Temporal's to own (§3),
// exactly like Trigger schedules (ADR-0010). The graph.baseline table is the
// projection of the Git declaration; the Schedule is a further projection
// reconciled from it, creates and deletes both (§1.2).
package baselines

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"

	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/core/internal/orchestrate"
	"github.com/dstout-devops/stratt/types"
)

// ScheduleIDPrefix namespaces Stratt-owned Baseline cadence Schedules.
const ScheduleIDPrefix = "stratt-baseline-"

// hashMemoKey carries the declaration hash on the schedule's workflow action
// (replaced whole on update — never stale, unlike the create-only Memo).
const hashMemoKey = "strattBaselineHash"

// ScheduleID returns the Temporal Schedule id for a Baseline name.
func ScheduleID(name string) string { return ScheduleIDPrefix + name }

// Reconciler converges Temporal Schedules onto the declared Baselines.
type Reconciler struct {
	Temporal client.Client
	Store    *graph.Store
	Log      *slog.Logger
	// Interval between reconcile cycles; <=0 means 30s.
	Interval time.Duration
}

// Run reconciles until ctx ends.
func (r *Reconciler) Run(ctx context.Context) error {
	log := r.Log.With("component", "baselines")
	interval := r.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	log.Info("baseline cadence reconciliation started", "interval", interval.String())
	for {
		if err := r.Reconcile(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("baseline reconcile failed", "error", err)
		}
		select {
		case <-time.After(interval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Reconcile performs one convergence pass. Per-baseline failures are logged
// and skipped so one bad declaration never blocks the rest.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	log := r.Log.With("component", "baselines")
	desired, err := r.Store.ListBaselines(ctx)
	if err != nil {
		return err
	}
	want := map[string]types.Baseline{}
	for _, b := range desired {
		want[b.Name] = b
	}

	sc := r.Temporal.ScheduleClient()
	existing := map[string]bool{}
	iter, err := sc.List(ctx, client.ScheduleListOptions{})
	if err != nil {
		return fmt.Errorf("baselines: list schedules: %w", err)
	}
	for iter.HasNext() {
		entry, err := iter.Next()
		if err != nil {
			return fmt.Errorf("baselines: list schedules: %w", err)
		}
		name, ours := strings.CutPrefix(entry.ID, ScheduleIDPrefix)
		if !ours {
			continue // not a Stratt projection; never touched
		}
		if _, ok := want[name]; !ok {
			if err := sc.GetHandle(ctx, entry.ID).Delete(ctx); err != nil {
				log.Error("baseline schedule delete failed", "baseline", name, "error", err)
				continue
			}
			log.Info("baseline schedule deleted", "baseline", name)
			continue
		}
		existing[name] = true
	}

	for name, b := range want {
		var err error
		if existing[name] {
			err = r.converge(ctx, log, b)
		} else {
			err = r.create(ctx, b)
			if err == nil {
				log.Info("baseline schedule created", "baseline", name, "cron", b.Cron, "paused", b.Paused)
			}
		}
		if err != nil {
			log.Error("baseline schedule reconcile failed", "baseline", name, "error", err)
		}
	}
	return nil
}

// compile renders a Baseline declaration into its Schedule pieces.
func compile(b types.Baseline) (client.ScheduleSpec, *client.ScheduleWorkflowAction, string, error) {
	hash, err := declarationHash(b)
	if err != nil {
		return client.ScheduleSpec{}, nil, "", err
	}
	spec := client.ScheduleSpec{CronExpressions: []string{b.Cron}}
	action := &client.ScheduleWorkflowAction{
		ID:        "baseline-" + b.Name,
		TaskQueue: orchestrate.TaskQueue,
		Workflow:  orchestrate.RunBaselineCheck,
		Args:      []any{orchestrate.BaselineInput{BaselineName: b.Name}},
		Memo:      map[string]any{hashMemoKey: hash},
	}
	return spec, action, hash, nil
}

func (r *Reconciler) create(ctx context.Context, b types.Baseline) error {
	spec, action, _, err := compile(b)
	if err != nil {
		return err
	}
	_, err = r.Temporal.ScheduleClient().Create(ctx, client.ScheduleOptions{
		ID:     ScheduleID(b.Name),
		Spec:   spec,
		Action: action,
		// A check that lands while the previous one is still going is
		// skipped, visibly (§1.8) — checks never stack.
		Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_SKIP,
		Paused:  b.Paused,
	})
	if _, exists := err.(*serviceerror.AlreadyExists); exists {
		return nil // raced a concurrent pass; the next cycle converges it
	}
	return err
}

// converge updates an existing schedule when its recorded declaration hash
// or paused state drifts from the declaration.
func (r *Reconciler) converge(ctx context.Context, log *slog.Logger, b types.Baseline) error {
	spec, action, wantHash, err := compile(b)
	if err != nil {
		return err
	}
	handle := r.Temporal.ScheduleClient().GetHandle(ctx, ScheduleID(b.Name))
	desc, err := handle.Describe(ctx)
	if err != nil {
		return err
	}
	if actionHash(desc) == wantHash && schedulePaused(desc) == b.Paused {
		return nil
	}
	err = handle.Update(ctx, client.ScheduleUpdateOptions{
		DoUpdate: func(client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
			return &client.ScheduleUpdate{Schedule: &client.Schedule{
				Spec:   &spec,
				Action: action,
				Policy: &client.SchedulePolicies{Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_SKIP},
				State:  &client.ScheduleState{Paused: b.Paused},
			}}, nil
		},
	})
	if err == nil {
		log.Info("baseline schedule updated", "baseline", b.Name, "cron", b.Cron, "paused", b.Paused)
	}
	return err
}

// declarationHash is the canonical-JSON content hash of the declaration.
func declarationHash(b types.Baseline) (string, error) {
	doc, err := json.Marshal(b)
	if err != nil {
		return "", fmt.Errorf("baselines: %s: marshal declaration: %w", b.Name, err)
	}
	sum := sha256.Sum256(doc)
	return hex.EncodeToString(sum[:]), nil
}

// actionHash extracts the declaration hash recorded on the workflow action's
// memo; "" when absent or undecodable (which reads as drift → update).
func actionHash(desc *client.ScheduleDescription) string {
	wa, ok := desc.Schedule.Action.(*client.ScheduleWorkflowAction)
	if !ok || wa.Memo == nil {
		return ""
	}
	raw, ok := wa.Memo[hashMemoKey]
	if !ok {
		return ""
	}
	if p, isPayload := raw.(*commonpb.Payload); isPayload {
		var s string
		if converter.GetDefaultDataConverter().FromPayload(p, &s) == nil {
			return s
		}
		return ""
	}
	s, _ := raw.(string)
	return s
}

func schedulePaused(desc *client.ScheduleDescription) bool {
	return desc.Schedule.State != nil && desc.Schedule.State.Paused
}
