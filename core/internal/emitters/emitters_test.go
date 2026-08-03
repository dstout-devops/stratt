package emitters

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/desiredstate"
	"github.com/dstout-devops/stratt/types"
)

// The Alertmanager POST every test below works from. One envelope, three alerts, and a `status`
// at BOTH levels — which is the collision D3 exists for, present in the very payload that
// motivated the feature.
const amBody = `{
	"receiver":"stratt","status":"firing",
	"groupLabels":{"alertname":"HighLoad"},
	"alerts":[
		{"status":"firing","labels":{"severity":"critical","instance":"web-1"}},
		{"status":"firing","labels":{"severity":"warning","instance":"web-2"}},
		{"status":"resolved","labels":{"severity":"critical","instance":"web-3"}}
	]}`

// declaredAlertmanager returns the Emitter an estate gets from `kind: alertmanager`, through the
// ONE normalization that spelling survives as (ADR-0163 D2).
func declaredAlertmanager(name string) types.Emitter {
	e := types.Emitter{Name: name, Kind: types.EmitterAlertmanager}
	desiredstate.NormalizeEmitter(&e)
	return e
}

func TestExplodeWithNoDeclarationIsOneEventPerPost(t *testing.T) {
	// EVERY EMITTER THAT SHIPPED BEFORE THIS FIELD EXISTED IS THIS CASE. A source whose POSTs
	// silently started fanning out would break every rule matching against them.
	evs, err := Explode(context.Background(), types.Emitter{Name: "hooks", Kind: types.EmitterWebhook},
		[]byte(`{"severity":"critical","service":"web"}`))
	if err != nil || len(evs) != 1 || evs[0].Payload["service"] != "web" || evs[0].Emitter != "hooks" {
		t.Fatalf("webhook: %+v %v", evs, err)
	}
	// Including a body that HAS an array in it — fanning out is declared, never inferred.
	evs, err = Explode(context.Background(), types.Emitter{Name: "hooks", Kind: types.EmitterWebhook}, []byte(amBody))
	if err != nil || len(evs) != 1 {
		t.Fatalf("an undeclared array must not fan out: %d %v", len(evs), err)
	}

	if _, err := Explode(context.Background(), types.Emitter{Name: "hooks", Kind: types.EmitterWebhook}, []byte("not json")); err == nil {
		t.Fatal("non-JSON body must be rejected")
	}
}

// THE MIGRATION'S ENTIRE CLAIM. `kind: alertmanager` left core and became a declaration; the
// only thing that makes that safe rather than merely tidy is that the two produce the SAME
// events. The expectation below is a frozen copy of what the deleted Go struct built — written
// out by hand on purpose, because re-deriving it from the new code would prove nothing.
func TestTheDeclaredFormIsByteIdenticalToTheKindItReplaced(t *testing.T) {
	evs, err := Explode(context.Background(), declaredAlertmanager("alerts"), []byte(amBody))
	if err != nil {
		t.Fatalf("declared explosion: %v", err)
	}

	want := []map[string]any{
		{"status": "firing", "labels": map[string]any{"severity": "critical", "instance": "web-1"},
			"receiver": "stratt", "groupLabels": map[string]any{"alertname": "HighLoad"}},
		{"status": "firing", "labels": map[string]any{"severity": "warning", "instance": "web-2"},
			"receiver": "stratt", "groupLabels": map[string]any{"alertname": "HighLoad"}},
		{"status": "resolved", "labels": map[string]any{"severity": "critical", "instance": "web-3"},
			"receiver": "stratt", "groupLabels": map[string]any{"alertname": "HighLoad"}},
	}
	if len(evs) != len(want) {
		t.Fatalf("got %d events, want %d", len(evs), len(want))
	}
	for i := range want {
		got, _ := json.Marshal(evs[i].Payload)
		exp, _ := json.Marshal(want[i])
		if string(got) != string(exp) {
			t.Errorf("event %d differs from the kind this replaced:\n got %s\nwant %s", i, got, exp)
		}
		if evs[i].Emitter != "alerts" {
			t.Errorf("event %d: emitter = %q", i, evs[i].Emitter)
		}
	}
	// The alert's OWN status survives — the envelope's `status` is not merged, exactly as the
	// old code did not merge it. Getting this wrong would silently relabel every resolved alert.
	if evs[2].Payload["status"] != "resolved" {
		t.Errorf("the alert's own status must survive: %v", evs[2].Payload["status"])
	}
}

// A source core has never heard of, nesting its items somewhere else entirely — the whole point.
// No Go changed to make this work.
func TestASourceCoreHasNeverHeardOfFansOut(t *testing.T) {
	em := types.Emitter{Name: "nms", Kind: types.EmitterWebhook, Explode: &types.ExplodeSpec{
		Path:  "data.events",
		Merge: []types.MergeKey{{Path: "meta.site"}, {Path: "data.batchId", As: "batch"}},
	}}
	evs, err := Explode(context.Background(), em, []byte(`{
		"meta":{"site":"dc-3"},
		"data":{"batchId":"b-77","events":[{"port":"ge-0/0/1"},{"port":"ge-0/0/2"}]}}`))
	if err != nil {
		t.Fatalf("nested explosion: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("got %d events, want 2", len(evs))
	}
	p := evs[0].Payload
	if p["port"] != "ge-0/0/1" || p["site"] != "dc-3" || p["batch"] != "b-77" {
		t.Fatalf("a nested path and a renamed merge must both land: %+v", p)
	}
}

// A path that addresses nothing is REFUSED. Quietly yielding the un-exploded body is the failure
// that looks like success: every rule stops matching, nothing launches, and no surface says why.
func TestAPathThatAddressesNothingIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, path, body string }{
		{"absent", "alerts", `{"items":[{"a":1}]}`},
		{"not an array", "alerts", `{"alerts":{"a":1}}`},
		{"array of scalars", "alerts", `{"alerts":["a","b"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			em := types.Emitter{Name: "x", Kind: types.EmitterWebhook, Explode: &types.ExplodeSpec{Path: tc.path}}
			evs, err := Explode(context.Background(), em, []byte(tc.body))
			if err == nil {
				t.Fatalf("must be refused, got %d events", len(evs))
			}
			if !strings.Contains(err.Error(), tc.path) {
				t.Errorf("the refusal must name the path (§1.8): %v", err)
			}
		})
	}
}

// A merged key colliding with one the item already carries is REFUSED, not resolved — and this is
// reachable on a first POST rather than theoretical, because Alertmanager has `status` at both
// levels. Inventing a winner would be the implicit precedence §2.4 exists to refuse, and the loser
// would be invisible.
func TestAMergeCollisionIsRefusedAndNamesTheKey(t *testing.T) {
	collide := types.Emitter{Name: "alerts", Kind: types.EmitterWebhook, Explode: &types.ExplodeSpec{
		Path: "alerts", Merge: []types.MergeKey{{Path: "status"}},
	}}
	_, err := Explode(context.Background(), collide, []byte(amBody))
	if err == nil {
		t.Fatal("merging `status` onto an alert that has one must be refused")
	}
	if !strings.Contains(err.Error(), "status") || !strings.Contains(err.Error(), "as:") {
		t.Errorf("the refusal must name the key AND the way out: %v", err)
	}

	// …and `as:` is that way out.
	renamed := types.Emitter{Name: "alerts", Kind: types.EmitterWebhook, Explode: &types.ExplodeSpec{
		Path: "alerts", Merge: []types.MergeKey{{Path: "status", As: "groupStatus"}},
	}}
	evs, err := Explode(context.Background(), renamed, []byte(amBody))
	if err != nil {
		t.Fatalf("`as:` must resolve the collision: %v", err)
	}
	if evs[2].Payload["status"] != "resolved" || evs[2].Payload["groupStatus"] != "firing" {
		t.Errorf("both must survive, apart: %+v", evs[2].Payload)
	}
}

// An envelope field this particular POST omitted is absent, not fatal: sources vary a shape
// between messages, and failing ingest over a missing optional would drop the alert.
func TestAnAbsentMergeFieldIsNotAnError(t *testing.T) {
	em := types.Emitter{Name: "x", Kind: types.EmitterWebhook, Explode: &types.ExplodeSpec{
		Path: "alerts", Merge: []types.MergeKey{{Path: "receiver"}},
	}}
	evs, err := Explode(context.Background(), em, []byte(`{"alerts":[{"a":1}]}`))
	if err != nil || len(evs) != 1 {
		t.Fatalf("absent merge field: %d %v", len(evs), err)
	}
	if _, present := evs[0].Payload["receiver"]; present {
		t.Error("a field the POST did not carry must not be invented")
	}
}
