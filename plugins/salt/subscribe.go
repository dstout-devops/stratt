package salt

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// Subscribe streams the Salt event bus over the port (ADR-0046/0047 Emitter verb).
// It holds one long-lived stream, reconnecting to salt-api GET /events with
// backoff on any error — the plugin owns its upstream position; the core-side
// host owns the emitter NAME and trigger routing (a plugin cannot spoof the
// route). Each Salt event becomes an EmittedEvent whose core-legible `match`
// carries {tag, stamp, data} for CEL, while the opaque payload is the raw body.
func (s *Server) Subscribe(_ *pluginv1.SubscribeRequest, stream grpc.ServerStreamingServer[pluginv1.SubscribeResponse]) error {
	ctx := stream.Context()
	client := newSaltClient(s.cfg)
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := s.streamEvents(ctx, client, stream)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.log.Warn("salt event stream ended; reconnecting", "error", err, "backoff", backoff.String())
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// streamEvents opens one /events SSE connection and forwards matching frames.
func (s *Server) streamEvents(ctx context.Context, client *saltClient, stream grpc.ServerStreamingServer[pluginv1.SubscribeResponse]) error {
	token, err := client.authToken(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.APIURL+"/events", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-Auth-Token", token)
	res, err := client.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close() //nolint:errcheck
	if res.StatusCode == http.StatusUnauthorized {
		client.mu.Lock()
		client.token = "" // force re-login on reconnect
		client.mu.Unlock()
		return fmt.Errorf("salt: /events: 401 (token cleared)")
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("salt: /events: %s", res.Status)
	}

	// SSE frames: "tag: <tag>\ndata: <json>\n\n". Accumulate until a blank line.
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // events can be large
	var tag, data string
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Text()
		switch {
		case line == "":
			if tag != "" {
				if ev, ok := s.toEmittedEvent(tag, data); ok {
					if err := stream.Send(&pluginv1.SubscribeResponse{Event: ev}); err != nil {
						return err
					}
				}
			}
			tag, data = "", ""
		case strings.HasPrefix(line, "tag:"):
			tag = strings.TrimSpace(strings.TrimPrefix(line, "tag:"))
		case strings.HasPrefix(line, "data:"):
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	return scanner.Err()
}

// toEmittedEvent builds the core-legible EmittedEvent from one Salt event, or
// (nil,false) when the tag is filtered out. `match` is the CEL projection; the
// opaque payload is the raw data body; occurred_at is the Salt _stamp (excluded
// from the host's dedup hash so genuinely-distinct events hash distinctly).
func (s *Server) toEmittedEvent(tag, dataJSON string) (*pluginv1.EmittedEvent, bool) {
	if !s.tagMatches(tag) {
		return nil, false
	}
	data := map[string]any{}
	if dataJSON != "" {
		if err := json.Unmarshal([]byte(dataJSON), &data); err != nil {
			s.log.Warn("skipping salt event; bad data json", "tag", tag, "error", err)
			return nil, false
		}
	}
	stamp := str(data, "_stamp")
	matchStruct, err := structpb.NewStruct(map[string]any{"tag": tag, "stamp": stamp, "data": data})
	if err != nil {
		s.log.Warn("skipping salt event; match not representable", "tag", tag, "error", err)
		return nil, false
	}
	ev := &pluginv1.EmittedEvent{
		Payload: &pluginv1.Payload{Bytes: []byte(dataJSON)},
		Match:   matchStruct,
		Subject: "salt",
		Type:    tag,
	}
	if t, perr := time.Parse(time.RFC3339Nano, stamp); perr == nil {
		ev.OccurredAt = timestamppb.New(t)
	}
	return ev, true
}

func (s *Server) tagMatches(tag string) bool {
	if len(s.cfg.EventTags) == 0 {
		return true
	}
	for _, p := range s.cfg.EventTags {
		if strings.HasPrefix(tag, p) {
			return true
		}
	}
	return false
}
