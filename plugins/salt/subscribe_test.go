package salt

import (
	"io"
	"log/slog"
	"testing"
)

func testServer(tags ...string) *Server {
	return &Server{cfg: Config{EventTags: tags}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// TestToEmittedEvent proves the Salt→EmittedEvent translation: the tag-prefix
// filter, the core-legible `match` {tag, stamp, data} for CEL, the typed
// subject/type, occurred_at from the Salt _stamp, and the opaque raw payload.
func TestToEmittedEvent(t *testing.T) {
	s := testServer("salt/job/")

	if _, ok := s.toEmittedEvent("salt/minion/refresh", `{}`); ok {
		t.Fatal("an event outside the tag allowlist must be filtered out")
	}

	ev, ok := s.toEmittedEvent("salt/job/20260716/ret", `{"_stamp":"2026-07-16T00:00:00Z","fun":"test.ping","id":"minion-1"}`)
	if !ok {
		t.Fatal("a tag matching the allowlist must emit")
	}
	m := ev.GetMatch().AsMap()
	if m["tag"] != "salt/job/20260716/ret" {
		t.Fatalf("match.tag = %v", m["tag"])
	}
	data, _ := m["data"].(map[string]any)
	if data["fun"] != "test.ping" || data["id"] != "minion-1" {
		t.Fatalf("match.data not legible for CEL: %v", data)
	}
	if ev.GetType() != "salt/job/20260716/ret" {
		t.Fatalf("type = %q", ev.GetType())
	}
	if ev.GetSubject() != "salt" {
		t.Fatalf("subject = %q", ev.GetSubject())
	}
	if ev.GetOccurredAt() == nil {
		t.Fatal("occurred_at must be parsed from the Salt _stamp")
	}
	if len(ev.GetPayload().GetBytes()) == 0 {
		t.Fatal("the opaque payload must carry the raw event body")
	}
}

// TestToEmittedEvent_NoFilter forwards all tags when no allowlist is set.
func TestToEmittedEvent_NoFilter(t *testing.T) {
	s := testServer()
	if _, ok := s.toEmittedEvent("anything/at/all", `{"_stamp":"2026-07-16T00:00:00Z"}`); !ok {
		t.Fatal("empty allowlist forwards all tags")
	}
}
