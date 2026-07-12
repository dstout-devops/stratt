package emitters

import (
	"context"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func TestExplode(t *testing.T) {
	// webhook: one event, payload = body.
	evs, err := Explode(context.Background(), types.Emitter{Name: "hooks", Kind: types.EmitterWebhook},
		[]byte(`{"severity":"critical","service":"web"}`))
	if err != nil || len(evs) != 1 || evs[0].Payload["service"] != "web" || evs[0].Emitter != "hooks" {
		t.Fatalf("webhook: %+v %v", evs, err)
	}

	// alertmanager: one event per alert, group fields folded in.
	am := []byte(`{
		"receiver":"stratt","status":"firing",
		"groupLabels":{"alertname":"HighLoad"},
		"alerts":[
			{"status":"firing","labels":{"severity":"critical","instance":"web-1"}},
			{"status":"firing","labels":{"severity":"warning","instance":"web-2"}},
			{"status":"resolved","labels":{"severity":"critical","instance":"web-3"}}
		]}`)
	evs, err = Explode(context.Background(), types.Emitter{Name: "alerts", Kind: types.EmitterAlertmanager}, am)
	if err != nil || len(evs) != 3 {
		t.Fatalf("alertmanager explosion: %d %v", len(evs), err)
	}
	first := evs[0].Payload
	if first["receiver"] != "stratt" || first["groupLabels"].(map[string]any)["alertname"] != "HighLoad" {
		t.Fatalf("group fields must fold into each alert: %+v", first)
	}
	if first["labels"].(map[string]any)["instance"] != "web-1" {
		t.Fatalf("alert fields: %+v", first)
	}

	// non-JSON body → error.
	if _, err := Explode(context.Background(), types.Emitter{Name: "hooks", Kind: types.EmitterWebhook}, []byte("not json")); err == nil {
		t.Fatal("non-JSON body must be rejected")
	}
}
