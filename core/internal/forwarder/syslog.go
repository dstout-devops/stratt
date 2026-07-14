package forwarder

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/dstout-devops/stratt/types"
)

// syslogDriver ships RFC 5424 messages over TCP (optionally TLS), octet-counted
// per RFC 6587 (ADR-0034). One frame per audit event; the message payload is
// the flattened event JSON. Default facility 13 ("log audit").
type syslogDriver struct {
	cfg SinkConfig
}

func (d *syslogDriver) Name() string { return types.SinkSyslog }

func (d *syslogDriver) Ship(ctx context.Context, events []types.AuditEvent) error {
	dialer := net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	var err error
	if d.cfg.TLS != nil {
		conn, err = tls.DialWithDialer(&dialer, "tcp", d.cfg.Endpoint, d.cfg.TLS)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", d.cfg.Endpoint)
	}
	if err != nil {
		return fmt.Errorf("syslog: dial failed") // sanitized (§2.5)
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))

	facility := d.cfg.Facility
	if facility == 0 {
		facility = 13 // log audit
	}
	for _, e := range events {
		msg, _ := json.Marshal(eventJSON(e))
		pri := facility*8 + severity(e)
		frame := fmt.Sprintf("<%d>1 %s stratt stratt-forwarder %d audit - %s",
			pri, e.At.UTC().Format(time.RFC3339Nano), e.Seq, msg)
		// RFC 6587 octet-counting: "<length> <frame>".
		if _, err := fmt.Fprintf(conn, "%d %s", len(frame), frame); err != nil {
			return fmt.Errorf("syslog: write failed at seq %d", e.Seq)
		}
	}
	return nil
}

// severity maps an audit outcome to an RFC 5424 severity: denied/failed are
// warnings (4), everything else informational (6).
func severity(e types.AuditEvent) int {
	switch e.Outcome {
	case types.AuditDenied, types.AuditFailed:
		return 4
	default:
		return 6
	}
}
