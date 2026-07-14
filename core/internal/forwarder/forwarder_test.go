package forwarder

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dstout-devops/stratt/types"
)

func evs() []types.AuditEvent {
	at := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	return []types.AuditEvent{
		{Seq: 1, At: at, PrincipalID: "alice", Action: "run.start", Object: "view:prod", Outcome: "ok"},
		{Seq: 2, At: at, PrincipalID: "mallory", Action: "authz.exec-grant", Object: "view:prod", Outcome: "denied"},
	}
}

func TestSplunkDriver(t *testing.T) {
	var body string
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/services/collector/event") {
			t.Errorf("path %s", r.URL.Path)
		}
		auth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	d, _ := NewDriver(SinkConfig{Kind: types.SinkSplunkHEC, Endpoint: srv.URL, Index: "audit", Token: "sekret"})
	if err := d.Ship(context.Background(), evs()); err != nil {
		t.Fatal(err)
	}
	if auth != "Splunk sekret" {
		t.Fatalf("auth header: %q", auth)
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 HEC events, got %d:\n%s", len(lines), body)
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first["index"] != "audit" || first["sourcetype"] != "stratt:audit" {
		t.Fatalf("hec envelope: %v", first)
	}
	if ev := first["event"].(map[string]any); ev["action"] != "run.start" || ev["principal"] != "alice" {
		t.Fatalf("hec event: %v", ev)
	}
}

func TestSplunkDriverRejects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer srv.Close()
	d, _ := NewDriver(SinkConfig{Kind: types.SinkSplunkHEC, Endpoint: srv.URL})
	if err := d.Ship(context.Background(), evs()); err == nil {
		t.Fatal("a non-2xx must be a shipping error (retry, never advance)")
	}
}

func TestOTelDriver(t *testing.T) {
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/logs") {
			t.Errorf("path %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	d, _ := NewDriver(SinkConfig{Kind: types.SinkOTelLogs, Endpoint: srv.URL})
	if err := d.Ship(context.Background(), evs()); err != nil {
		t.Fatal(err)
	}
	rl := payload["resourceLogs"].([]any)
	sl := rl[0].(map[string]any)["scopeLogs"].([]any)
	recs := sl[0].(map[string]any)["logRecords"].([]any)
	if len(recs) != 2 {
		t.Fatalf("expected 2 log records, got %d", len(recs))
	}
	r0 := recs[0].(map[string]any)
	if r0["severityText"] != "INFO" {
		t.Fatalf("severity: %v", r0["severityText"])
	}
	if _, ok := r0["timeUnixNano"].(string); !ok {
		t.Fatalf("timeUnixNano must be a JSON string (OTLP): %T", r0["timeUnixNano"])
	}
}

func TestSyslogDriver(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		b, _ := io.ReadAll(bufio.NewReader(c))
		got <- string(b)
	}()

	d, _ := NewDriver(SinkConfig{Kind: types.SinkSyslog, Endpoint: ln.Addr().String(), Facility: 13})
	if err := d.Ship(context.Background(), evs()); err != nil {
		t.Fatal(err)
	}
	select {
	case frames := <-got:
		// RFC 6587 octet-counting: "<len> <frame>". PRI for a denied event =
		// facility*8 + 4 = 108; for ok = 110.
		if !strings.Contains(frames, "<110>1 ") || !strings.Contains(frames, "<108>1 ") {
			t.Fatalf("expected both severities framed: %s", frames)
		}
		if !strings.Contains(frames, `"action":"run.start"`) {
			t.Fatalf("event json missing: %s", frames)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no syslog frames received")
	}
}

// TestForwarderLoop exercises the whole loop against a mock platform API and a
// mock SIEM: it ships the batch, reports delivered, and the server advances the
// cursor so the next poll is empty — at-least-once, cursor server-owned.
func TestForwarderLoop(t *testing.T) {
	var mu sync.Mutex
	shipped := 0
	siem := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		shipped += strings.Count(strings.TrimSpace(string(b)), "\n") + 1
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer siem.Close()

	var reportedThrough int64
	delivered := make(chan struct{}, 1)
	served := false
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/config"):
			json.NewEncoder(w).Encode(map[string]any{"kind": types.SinkSplunkHEC, "endpoint": siem.URL, "insecure": true})
		case strings.HasSuffix(r.URL.Path, "/report"):
			var rep map[string]any
			_ = json.NewDecoder(r.Body).Decode(&rep)
			mu.Lock()
			reportedThrough = int64(rep["throughSeq"].(float64))
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			if rep["status"] == types.ForwardDelivered {
				select {
				case delivered <- struct{}{}:
				default:
				}
			}
		default: // the batch endpoint: serve once, then empty (cursor advanced)
			mu.Lock()
			first := !served
			served = true
			mu.Unlock()
			if first {
				json.NewEncoder(w).Encode(evs())
			} else {
				w.Write([]byte("[]"))
			}
		}
	}))
	defer platform.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := Config{Server: platform.URL, Sink: "s", Interval: 20 * time.Millisecond, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	go func() { _ = cfg.Run(ctx) }()

	select {
	case <-delivered:
	case <-time.After(5 * time.Second):
		t.Fatal("forwarder did not deliver")
	}
	mu.Lock()
	defer mu.Unlock()
	if shipped != 2 || reportedThrough != 2 {
		t.Fatalf("shipped=%d reportedThrough=%d", shipped, reportedThrough)
	}
}
