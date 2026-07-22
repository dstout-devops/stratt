package helm

import (
	"strings"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// lineToEvent maps one raw helm stdout/stderr line onto the port as a typed
// TaskEvent (the §1.8 descent channel — nothing helm prints is dropped). Unlike
// tofu, helm has no machine-readable -json stream, so classification is by content:
// an "Error:"/"error:" line is a diagnostic at ERROR level; a "WARNING:" line is
// WARN; everything else is an INFO log line. Returns nil for a blank line (pure
// whitespace carries no diagnosis and only adds stream noise).
func lineToEvent(seq int64, at *timestamppb.Timestamp, raw []byte) *pluginv1.TaskEvent {
	text := strings.TrimRight(string(raw), "\r\n")
	if strings.TrimSpace(text) == "" {
		return nil
	}
	level := pluginv1.TaskEvent_LEVEL_INFO
	kind := "helm"
	trimmed := strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(trimmed, "Error:"), strings.HasPrefix(trimmed, "error:"):
		level = pluginv1.TaskEvent_LEVEL_ERROR
		kind = "diagnostic"
	case strings.HasPrefix(trimmed, "WARNING:"), strings.Contains(trimmed, "[WARNING]"):
		level = pluginv1.TaskEvent_LEVEL_WARN
		kind = "diagnostic"
	}
	return &pluginv1.TaskEvent{
		Level:   level,
		Message: text,
		At:      at,
		Fields:  map[string]string{"kind": kind, "seq": itoa(seq)},
	}
}

func itoa(n int64) string {
	// small, allocation-light integer→string for the seq field (avoids strconv import
	// churn; seq is a monotonic non-negative counter).
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
