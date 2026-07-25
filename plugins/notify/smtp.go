package notify

import (
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// smtpArgs is the input Contract (actions/notify/smtp.input). The username and
// password are NOT here — they resolve from the Sink's CredentialRef via the
// SecretBroker (§2.5), exactly as the webhook driver's url/token do.
type smtpArgs struct {
	Body            string   `json:"body"`
	CredentialMount string   `json:"credentialMount"`
	Host            string   `json:"host"`
	Port            int      `json:"port"`
	From            string   `json:"from"`
	To              []string `json:"to"`
	Subject         string   `json:"subject"`
	TLS             string   `json:"tls"`
}

// smtpDialTimeout bounds the connect; the whole Invoke is additionally bounded
// by the Action's activity timeout on the core side.
const smtpDialTimeout = 15 * time.Second

// sendMail delivers one message and returns a SANITIZED verdict. Like the
// webhook driver's post(), it NEVER returns a raw error: an SMTP error string
// routinely embeds the relay host, the envelope sender, and the recipient list,
// and a delivery verdict is a control-plane surface (§2.5). Only the failure
// class crosses — enough to diagnose which stage failed (§1.8), never enough to
// leak the target.
func (s *Server) sendMail(ctx context.Context, a smtpArgs, user, pass string) (bool, string) {
	port := a.Port
	if port == 0 {
		// The two conventional defaults, chosen by the transport rather than by
		// a magic constant: implicit TLS is 465, STARTTLS/plain submission 587.
		port = 587
		if a.TLS == tlsImplicit {
			port = 465
		}
	}
	addr := net.JoinHostPort(a.Host, strconv.Itoa(port))

	c, stage, err := s.smtpClient(ctx, a, addr)
	if err != nil {
		return false, stage
	}
	defer func() { _ = c.Close() }()

	// Authenticate only when the relay offers it AND we hold material. PlainAuth
	// itself refuses to send credentials over an unencrypted connection — a
	// stdlib fail-closed we rely on rather than reimplement, and the reason
	// `tls: none` is a plain-relay-only mode by construction.
	if user != "" {
		ok, _ := c.Extension("AUTH")
		if !ok {
			return false, "relay offers no AUTH"
		}
		if err := c.Auth(smtp.PlainAuth("", user, pass, a.Host)); err != nil {
			return false, "auth rejected"
		}
	}
	if err := c.Mail(a.From); err != nil {
		return false, "sender rejected"
	}
	for _, rcpt := range a.To {
		if err := c.Rcpt(rcpt); err != nil {
			return false, "recipient rejected"
		}
	}
	w, err := c.Data()
	if err != nil {
		return false, "relay refused the message"
	}
	if _, err := w.Write([]byte(buildMessage(a))); err != nil {
		return false, "write failed"
	}
	if err := w.Close(); err != nil {
		// The close is where a relay reports its final accept/reject verdict, so
		// this — not Data() — is the "was it accepted" moment.
		return false, "relay rejected the message"
	}
	_ = c.Quit()
	return true, ""
}

// TLS modes. starttls is the default because it is what submission relays speak;
// none exists for a plain in-cluster relay and carries no credential by virtue
// of PlainAuth's own refusal.
const (
	tlsStartTLS = "starttls"
	tlsImplicit = "implicit"
	tlsNone     = "none"
)

// smtpClient dials and brings the connection to the point where AUTH is legal,
// returning a sanitized stage name on failure.
func (s *Server) smtpClient(ctx context.Context, a smtpArgs, addr string) (*smtp.Client, string, error) {
	d := &net.Dialer{Timeout: smtpDialTimeout}
	if a.TLS == tlsImplicit {
		td := &tls.Dialer{NetDialer: d, Config: &tls.Config{ServerName: a.Host, MinVersion: tls.VersionTLS12}}
		conn, err := td.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, "tls connect failed", err
		}
		c, err := smtp.NewClient(conn, a.Host)
		if err != nil {
			_ = conn.Close()
			return nil, "smtp handshake failed", err
		}
		return c, "", nil
	}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, "connect failed", err
	}
	c, err := smtp.NewClient(conn, a.Host)
	if err != nil {
		_ = conn.Close()
		return nil, "smtp handshake failed", err
	}
	if a.TLS == tlsNone {
		return c, "", nil
	}
	// Default (starttls): STARTTLS is REQUIRED, not attempted-then-shrugged-off.
	// A relay that does not offer it fails the delivery rather than silently
	// downgrading to cleartext — the downgrade is the whole attack, and a
	// notification body carries estate detail even when the credential does not.
	if ok, _ := c.Extension("STARTTLS"); !ok {
		_ = c.Close()
		return nil, "relay does not offer STARTTLS", fmt.Errorf("no starttls")
	}
	if err := c.StartTLS(&tls.Config{ServerName: a.Host, MinVersion: tls.VersionTLS12}); err != nil {
		_ = c.Close()
		return nil, "starttls failed", err
	}
	return c, "", nil
}

// buildMessage renders the RFC 5322 message. CR/LF is stripped from every header
// value, because an unescaped newline in one starts a new header — the email
// analogue of response-splitting, which the webhook driver never has to think
// about. Defense in depth rather than a live exposure: from/to/subject are Sink
// params, so they are Git-declared, and the one value that IS rendered from a
// Notice (the body) sits below the header block.
func buildMessage(a smtpArgs) string {
	var b strings.Builder
	b.WriteString("From: " + headerSafe(a.From) + "\r\n")
	b.WriteString("To: " + headerSafe(strings.Join(a.To, ", ")) + "\r\n")
	if a.Subject != "" {
		b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", headerSafe(a.Subject)) + "\r\n")
	}
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	// The body is written verbatim: c.Data() hands back a textproto DotWriter,
	// which already does the dot-stuffing and the CRLF normalization. Doing it
	// here too would double-escape a leading "." — the classic double-fix bug.
	b.WriteString(a.Body)
	return b.String()
}

// headerSafe strips CR and LF so a rendered value cannot inject a header.
func headerSafe(v string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
}
