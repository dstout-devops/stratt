// Package triggers reconciles declared Triggers (charter §2, ADR-0010) onto
// Temporal Schedules (§3: Temporal owns all lifecycle). The graph.trigger
// table is the projection of the Git declaration; the Temporal Schedule is a
// further projection reconciled from it — desired-state diff, creates and
// deletes both, so an out-of-band schedule under our prefix is removed the
// same way an out-of-band OpenFGA tuple is (§1.2).
package triggers

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

// ScheduleIDPrefix namespaces Stratt-owned Temporal Schedules; everything
// under it is a projection this reconciler may create and delete.
const ScheduleIDPrefix = "stratt-trigger-"

// hashMemoKey carries the declaration hash on the schedule's workflow action.
// The action is replaced whole on update, so the hash can never go stale —
// unlike the schedule-level Memo, which is create-only.
const hashMemoKey = "strattTriggerHash"

// ScheduleID returns the Temporal Schedule id for a Trigger name.
func ScheduleID(name string) string { return ScheduleIDPrefix + name }

// Reconciler converges Temporal Schedules onto the declared Triggers.
type Reconciler struct {
	Temporal client.Client
	Store    *graph.Store
	Log      *slog.Logger
	// Interval between reconcile cycles; <=0 means 30s (the desired-state
	// controller default — strattd passes the same value to both).
	Interval time.Duration
}

// Run reconciles until ctx ends.
func (r *Reconciler) Run(ctx context.Context) error {
	log := r.Log.With("component", "triggers")
	interval := r.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	log.Info("trigger schedule reconciliation started", "interval", interval.String())
	for {
		if err := r.Reconcile(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("trigger reconcile failed", "error", err)
		}
		select {
		case <-time.After(interval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Reconcile performs one convergence pass. Per-trigger failures are logged
// and skipped so one bad declaration (e.g. a cron Temporal rejects) never
// blocks the rest — the desired-state posture throughout.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	log := r.Log.With("component", "triggers")
	desired, err := r.Store.ListTriggers(ctx)
	if err != nil {
		return err
	}
	want := map[string]types.Trigger{}
	for _, t := range desired {
		// Only schedule-kind Triggers project onto Temporal Schedules;
		// event-kind Triggers belong to the trigger engine (ADR-0018).
		if t.Kind == types.TriggerSchedule {
			want[t.Name] = t
		}
	}

	sc := r.Temporal.ScheduleClient()
	existing := map[string]bool{} // trigger name → schedule exists
	iter, err := sc.List(ctx, client.ScheduleListOptions{})
	if err != nil {
		return fmt.Errorf("triggers: list schedules: %w", err)
	}
	for iter.HasNext() {
		entry, err := iter.Next()
		if err != nil {
			return fmt.Errorf("triggers: list schedules: %w", err)
		}
		name, ours := strings.CutPrefix(entry.ID, ScheduleIDPrefix)
		if !ours {
			continue // not a Stratt projection; never touched
		}
		if _, ok := want[name]; !ok {
			// Undeclared (pruned from Git, or created out-of-band under our
			// prefix): the projection is removed, same as an OpenFGA revoke.
			if err := sc.GetHandle(ctx, entry.ID).Delete(ctx); err != nil {
				log.Error("trigger schedule delete failed", "trigger", name, "error", err)
				continue
			}
			log.Info("trigger schedule deleted", "trigger", name)
			continue
		}
		existing[name] = true
	}

	for name, t := range want {
		var err error
		if existing[name] {
			err = r.converge(ctx, log, t)
		} else {
			err = r.create(ctx, t)
			if err == nil {
				log.Info("trigger schedule created", "trigger", name, "cron", t.Cron, "paused", t.Paused)
			}
		}
		if err != nil {
			log.Error("trigger schedule reconcile failed", "trigger", name, "error", err)
		}
	}
	return nil
}

// compile renders a Trigger declaration into the Temporal Schedule pieces:
// the spec, the workflow action (carrying the declaration hash in its memo),
// and the hash itself.
func compile(t types.Trigger) (client.ScheduleSpec, *client.ScheduleWorkflowAction, string, error) {
	var params json.RawMessage
	if t.Params != nil {
		raw, err := json.Marshal(t.Params)
		if err != nil {
			return client.ScheduleSpec{}, nil, "", fmt.Errorf("triggers: %s: marshal params: %w", t.Name, err)
		}
		params = raw
	}
	hash, err := declarationHash(t)
	if err != nil {
		return client.ScheduleSpec{}, nil, "", err
	}
	spec := client.ScheduleSpec{CronExpressions: []string{t.Cron}}
	action := &client.ScheduleWorkflowAction{
		ID:        "trigger-" + t.Name,
		TaskQueue: orchestrate.TaskQueue,
		Memo:      map[string]any{hashMemoKey: hash},
	}
	if t.WorkflowName != "" {
		// Workflow-launching schedule (the ADR-0010 rider): RunDAG creates
		// its own execution row via EnsureWorkflowRun.
		action.Workflow = orchestrate.RunDAG
		action.Args = []any{orchestrate.DAGInput{
			WorkflowName: t.WorkflowName,
			Principal:    t.Principal,
			Trigger:      t.Name,
		}}
	} else {
		action.Workflow = orchestrate.RunAgainstView
		action.Args = []any{orchestrate.RunInput{
			// RunID stays empty: EnsureRun creates the Run summary for
			// schedule-fired executions (ADR-0010).
			Trigger:  t.Name,
			ViewName: t.ViewName,
			// Schedule launches have no firing event; viewParams carry only
			// literal values (event templates are rejected at declaration,
			// ADR-0024).
			ViewParams:     t.ViewParams,
			Actuator:       t.Actuator,
			Params:         params,
			Slices:         t.Slices,
			Principal:      t.Principal,
			CredentialRefs: t.CredentialRefs,
		}}
	}
	return spec, action, hash, nil
}

func (r *Reconciler) create(ctx context.Context, t types.Trigger) error {
	spec, action, _, err := compile(t)
	if err != nil {
		return err
	}
	_, err = r.Temporal.ScheduleClient().Create(ctx, client.ScheduleOptions{
		ID:     ScheduleID(t.Name),
		Spec:   spec,
		Action: action,
		// An estate Run must never stack on itself; a fire that lands while
		// the previous Run is still going is skipped, and the skip is visible
		// in the schedule's info (§1.8 — visible mechanism, not hidden).
		Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_SKIP,
		Paused:  t.Paused,
	})
	if _, exists := err.(*serviceerror.AlreadyExists); exists {
		return nil // raced a concurrent pass; the next cycle converges it
	}
	return err
}

// converge updates an existing schedule when its recorded declaration hash or
// paused state drifts from the declaration. Cron does not round-trip through
// Describe (the server compiles it to a structured calendar), so the hash on
// the action memo is the drift signal.
func (r *Reconciler) converge(ctx context.Context, log *slog.Logger, t types.Trigger) error {
	spec, action, wantHash, err := compile(t)
	if err != nil {
		return err
	}
	handle := r.Temporal.ScheduleClient().GetHandle(ctx, ScheduleID(t.Name))
	desc, err := handle.Describe(ctx)
	if err != nil {
		return err
	}
	if actionHash(desc) == wantHash && schedulePaused(desc) == t.Paused {
		return nil
	}
	err = handle.Update(ctx, client.ScheduleUpdateOptions{
		DoUpdate: func(client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
			return &client.ScheduleUpdate{Schedule: &client.Schedule{
				Spec:   &spec,
				Action: action,
				Policy: &client.SchedulePolicies{Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_SKIP},
				State:  &client.ScheduleState{Paused: t.Paused},
			}}, nil
		},
	})
	if err == nil {
		log.Info("trigger schedule updated", "trigger", t.Name, "cron", t.Cron, "paused", t.Paused)
	}
	return err
}

// declarationHash is the canonical-JSON content hash of the declaration.
func declarationHash(t types.Trigger) (string, error) {
	doc, err := json.Marshal(t)
	if err != nil {
		return "", fmt.Errorf("triggers: %s: marshal declaration: %w", t.Name, err)
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
	// Describe returns memo values as payloads.
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
