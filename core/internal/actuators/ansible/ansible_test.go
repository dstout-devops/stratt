package ansible

import (
	"testing"
)

func TestParseEventAndFacts(t *testing.T) {
	// Shape emitted by `ansible-runner run -j`: one JSON event per line.
	line := []byte(`{"uuid":"u1","counter":7,"event":"runner_on_ok","stdout":"ok: [vm-1]","event_data":{"host":"vm-1","res":{"ansible_facts":{"ansible_system":"Linux","ansible_kernel":"6.6.87","ansible_architecture":"x86_64"}}}}`)
	ev, ok := ParseEvent(line)
	if !ok {
		t.Fatal("event line did not parse")
	}
	if ev.Event != "runner_on_ok" || ev.Counter != 7 {
		t.Fatalf("unexpected event: %+v", ev)
	}

	re := ToRunEvent("run-1", ev)
	if re.Target != "vm-1" || re.Seq != 7 || re.Kind != "runner_on_ok" {
		t.Fatalf("unexpected run event: %+v", re)
	}

	host, okRes, failed := HostResult(ev)
	if host != "vm-1" || !okRes || failed {
		t.Fatalf("unexpected host result: %s ok=%v failed=%v", host, okRes, failed)
	}

	facts := ExtractFacts(ev)
	if facts == nil {
		t.Fatal("expected facts")
	}
	if string(facts["os.kernel"]) != `{"arch":"x86_64","family":"linux","release":"6.6.87"}` {
		t.Fatalf("unexpected os.kernel facet: %s", facts["os.kernel"])
	}

	// Non-event noise must be ignored, not fail.
	if _, ok := ParseEvent([]byte("some banner text")); ok {
		t.Fatal("noise line should not parse as an event")
	}
}

func TestBuildContent(t *testing.T) {
	c := BuildContent(GatherFactsPlay, []Target{
		{EntityID: "e1", Name: "vm-1", Vars: map[string]string{"ansible_connection": "local"}},
		{EntityID: "e2", Name: "vm-2", Vars: map[string]string{"ansible_connection": "local"}},
	})
	want := "[all]\nvm-1 ansible_connection=local\nvm-2 ansible_connection=local\n"
	if c.Hosts != want {
		t.Fatalf("hosts:\n got %q\nwant %q", c.Hosts, want)
	}
	if c.Play == "" {
		t.Fatal("empty play")
	}
}
