package awxfacade

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dstout-devops/stratt/types"
)

var errStdoutDone = errors.New("stdout: stream-end")

// jobStdout: GET /api/v2/jobs/{id}/stdout/?format=txt|json — the concatenated
// tool stdout, drained from the Run's NATS event stream (Bus.Tail replays from
// the first event). Exits early on the stream-end marker; a still-running Run
// returns the output buffered so far (a short drain window), matching AWX's
// partial-stdout behavior.
func (f *Facade) jobStdout(w http.ResponseWriter, r *http.Request) {
	run, ok := f.runByPathID(w, r)
	if !ok {
		return
	}

	// Bound the drain: the backlog replays fast; stream-end exits early. A
	// running Run just returns what has streamed within the window.
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	var b strings.Builder
	err := f.cfg.Bus.Tail(ctx, run.ID, func(ev types.RunEvent) error {
		if ev.Kind == "stream-end" {
			return errStdoutDone
		}
		if s, ok := ev.Payload["stdout"].(string); ok && s != "" {
			b.WriteString(s)
			if !strings.HasSuffix(s, "\n") {
				b.WriteByte('\n')
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStdoutDone) && !errors.Is(err, context.DeadlineExceeded) {
		awxErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	content := b.String()
	if r.URL.Query().Get("format") == "json" {
		writeJSON(w, http.StatusOK, map[string]any{
			"range":   map[string]any{"start": 0, "end": len(content), "absolute_end": len(content)},
			"content": content,
		})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(content))
}
