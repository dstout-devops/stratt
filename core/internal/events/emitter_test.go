package events

import (
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func TestEventHashStability(t *testing.T) {
	a := types.EmitterEvent{Emitter: "hooks", ReceivedAt: "2026-07-12T01:00:00Z",
		Payload: map[string]any{"severity": "critical", "service": "web"}}
	b := types.EmitterEvent{Emitter: "hooks", ReceivedAt: "2026-07-12T09:99:99Z", // different time
		Payload: map[string]any{"severity": "critical", "service": "web"}}
	if EventHash(a) != EventHash(b) {
		t.Fatal("hash must ignore ReceivedAt (caller retries must dedup)")
	}
	c := types.EmitterEvent{Emitter: "other", Payload: a.Payload}
	if EventHash(a) == EventHash(c) {
		t.Fatal("hash must include the emitter")
	}
	d := types.EmitterEvent{Emitter: "hooks", Payload: map[string]any{"severity": "warning"}}
	if EventHash(a) == EventHash(d) {
		t.Fatal("hash must include the payload")
	}
}
