package notify

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dstout-devops/stratt/sdk/secretbroker"
	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// fakeRelay is a minimal SMTP responder — enough of RFC 5321 to complete one
// transaction and hand the test back the DATA it received. `starttls` controls
// whether the EHLO banner advertises STARTTLS, which is how the downgrade guard
// is exercised without standing up TLS.
type fakeRelay struct {
	ln       net.Listener
	starttls bool

	mu   sync.Mutex
	data string
	from string
	rcpt []string
	auth string
}

func newRelay(t *testing.T, advertiseSTARTTLS bool) *fakeRelay {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &fakeRelay{ln: ln, starttls: advertiseSTARTTLS}
	go r.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return r
}

func (r *fakeRelay) addr() (string, int) {
	a := r.ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", a.Port
}

func (r *fakeRelay) received() (string, string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.data, r.from, append([]string(nil), r.rcpt...)
}

func (r *fakeRelay) serve() {
	for {
		conn, err := r.ln.Accept()
		if err != nil {
			return
		}
		go r.handle(conn)
	}
}

func (r *fakeRelay) handle(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	w := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	w("220 relay.test ESMTP")
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			w("250-relay.test")
			if r.starttls {
				w("250-STARTTLS")
			}
			w("250 AUTH PLAIN")
		case strings.HasPrefix(cmd, "AUTH PLAIN"):
			r.mu.Lock()
			r.auth = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "AUTH PLAIN"))
			r.mu.Unlock()
			w("235 authenticated")
		case strings.HasPrefix(cmd, "MAIL FROM"):
			r.mu.Lock()
			r.from = strings.TrimSpace(line)
			r.mu.Unlock()
			w("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO"):
			r.mu.Lock()
			r.rcpt = append(r.rcpt, strings.TrimSpace(line))
			r.mu.Unlock()
			w("250 ok")
		case cmd == "DATA":
			w("354 send it")
			var b strings.Builder
			for {
				l, err := br.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			r.mu.Lock()
			r.data = b.String()
			r.mu.Unlock()
			w("250 queued")
		case cmd == "QUIT":
			w("221 bye")
			return
		default:
			w("250 ok")
		}
	}
}

func smtpServer(t *testing.T) *Server {
	t.Helper()
	cs := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "stratt-secrets", Name: "smtp-sink"},
		Data:       map[string][]byte{"username": []byte("mailer"), "password": []byte("s3cr3t")},
	})
	return New("notify", secretbroker.New(cs, "stratt-secrets"), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func smtpRef() *pluginv1.CredentialRef {
	return &pluginv1.CredentialRef{
		Name: "cred/smtp",
		Resolved: &pluginv1.ResolvedRef{
			SecretNamespace: "stratt-secrets", SecretName: "smtp-sink",
			Keys: []*pluginv1.ResolvedKey{{Key: "username", Name: "username"}, {Key: "password", Name: "password"}},
		},
	}
}

func invokeSMTPAction(s *Server, args any, creds ...*pluginv1.CredentialRef) *fakeStream {
	raw, _ := json.Marshal(args)
	st := &fakeStream{ctx: context.Background()}
	_ = s.Invoke(&pluginv1.InvokeRequest{
		Envelope: &pluginv1.Envelope{Creds: creds},
		Args:     &pluginv1.Payload{Bytes: raw},
		Action:   actionSMTP,
	}, st)
	return st
}

// The seam's proof (ADR-0125 D4): a second driver, on a transport that is not
// HTTP, delivers through the same Invoke shape the webhook driver uses — and
// core dispatched it without knowing SMTP exists.
func TestInvokeSMTP_DeliversOverANonHTTPTransport(t *testing.T) {
	relay := newRelay(t, false)
	host, port := relay.addr()

	st := invokeSMTPAction(smtpServer(t), map[string]any{
		"body": "run r-1 failed on prod", "credentialMount": "cred/smtp",
		"host": host, "port": port, "tls": "none",
		"from": "stratt@example.test", "to": []string{"ops@example.test", "sre@example.test"},
		"subject": "Stratt: run failed",
	}, smtpRef())

	term := st.terminal()
	if term == nil || !term.GetOk() {
		t.Fatalf("delivery must end with a terminal ok, got %+v", term)
	}
	data, from, rcpt := relay.received()
	// The brokered material actually reached the relay — the SMTP mirror of the
	// webhook test's `Bearer s3cr3t` assertion. Without it, "delivered" could mean
	// the driver skipped auth and the relay happened not to care.
	relay.mu.Lock()
	authed := relay.auth
	relay.mu.Unlock()
	creds, _ := base64.StdEncoding.DecodeString(authed)
	if !strings.Contains(string(creds), "mailer") || !strings.Contains(string(creds), "s3cr3t") {
		t.Errorf("the brokered username/password must reach the relay, got %q", authed)
	}
	if !strings.Contains(from, "stratt@example.test") {
		t.Errorf("envelope sender wrong: %q", from)
	}
	if len(rcpt) != 2 {
		t.Errorf("every recipient must get an RCPT TO, got %v", rcpt)
	}
	if !strings.Contains(data, "Subject: Stratt: run failed") {
		t.Errorf("subject header missing: %q", data)
	}
	if !strings.Contains(data, "run r-1 failed on prod") {
		t.Errorf("rendered body missing: %q", data)
	}
}

// A CRLF in a header value would inject arbitrary headers — Bcc a third party,
// rewrite Content-Type. This is defense in depth rather than a live exposure:
// from/to/subject are Sink params, so they are Git-declared and reviewed, unlike
// the body. It is asserted anyway because the cost is one Replacer and the
// failure is silent.
//
// The test targets `from` deliberately. Subject is ALSO protected — by
// mime.QEncoding, which escapes any byte below 0x20 — so asserting on Subject
// would pass with headerSafe deleted and prove nothing. from/to are the values
// headerSafe alone protects.
func TestBuildMessageStripsHeaderInjection(t *testing.T) {
	msg := buildMessage(smtpArgs{
		From: "stratt@example.test\r\nBcc: attacker@evil.test",
		To:   []string{"ops@example.test"}, Subject: "hi", Body: "body",
	})
	headers, _, _ := strings.Cut(msg, "\r\n\r\n")
	for _, line := range strings.Split(headers, "\r\n") {
		if strings.HasPrefix(line, "Bcc:") {
			t.Fatalf("a CRLF in a header value must not start a new header:\n%s", headers)
		}
	}
	// Every header we emit and no more: From, To, Subject, Date, MIME-Version,
	// Content-Type.
	if got := len(strings.Split(headers, "\r\n")); got != 6 {
		t.Fatalf("expected exactly 6 headers, got %d:\n%s", got, headers)
	}
}

// STARTTLS is REQUIRED in the default mode, not attempted-then-shrugged-off: a
// relay that does not offer it fails the delivery rather than silently sending
// the notification in cleartext. A notification body carries estate detail (run
// ids, view names, Finding severities) even when the credential does not.
func TestSMTPRefusesToDowngradeFromSTARTTLS(t *testing.T) {
	relay := newRelay(t, false) // does NOT advertise STARTTLS
	host, port := relay.addr()

	st := invokeSMTPAction(smtpServer(t), map[string]any{
		"body": "x", "credentialMount": "cred/smtp", "host": host, "port": port,
		"from": "a@example.test", "to": []string{"b@example.test"},
	}, smtpRef()) // no tls: → defaults to starttls

	term := st.terminal()
	if term == nil || term.GetOk() {
		t.Fatalf("a relay with no STARTTLS must fail the delivery, got %+v", term)
	}
	if got := term.GetFields()["detail"]; got != "relay does not offer STARTTLS" {
		t.Fatalf("the refusal must say why (§1.8), got %q", got)
	}
	if data, _, _ := relay.received(); data != "" {
		t.Fatal("no message may be sent over the downgraded connection")
	}
}

// The verdict is a sanitized failure CLASS. An SMTP error string embeds the
// relay host and the envelope exactly as an HTTP error embeds the URL, and the
// verdict is a control-plane surface (§2.5) — so nothing target-identifying may
// ride it.
func TestSMTPFailureVerdictLeaksNoTarget(t *testing.T) {
	st := invokeSMTPAction(smtpServer(t), map[string]any{
		"body": "x", "credentialMount": "cred/smtp",
		"host": "relay.internal.example.test", "port": 1, "tls": "none",
		"from": "a@example.test", "to": []string{"b@example.test"},
	}, smtpRef())

	term := st.terminal()
	if term == nil || term.GetOk() {
		t.Fatalf("an unreachable relay must fail, got %+v", term)
	}
	detail := term.GetFields()["detail"]
	if detail != "connect failed" {
		t.Fatalf("verdict must be a failure class, got %q", detail)
	}
	if strings.Contains(detail, "relay.internal") || strings.Contains(detail, "example.test") {
		t.Fatalf("the verdict leaked the target: %q", detail)
	}
}

// Withheld coordinates fail closed on this driver too (MF-C) — the property is
// the SDK broker's, and this asserts the new driver actually goes through it
// rather than reading material some other way.
func TestInvokeSMTP_WithheldCoordinatesFailClosed(t *testing.T) {
	relay := newRelay(t, false)
	host, port := relay.addr()

	st := invokeSMTPAction(smtpServer(t), map[string]any{
		"body": "x", "credentialMount": "cred/smtp", "host": host, "port": port, "tls": "none",
		"from": "a@example.test", "to": []string{"b@example.test"},
	}, &pluginv1.CredentialRef{Name: "cred/smtp"}) // NAME only

	term := st.terminal()
	if term == nil || term.GetOk() {
		t.Fatalf("withheld coordinates must fail closed, got %+v", term)
	}
	if data, _, _ := relay.received(); data != "" {
		t.Fatal("no mail may be sent when the credential cannot be resolved")
	}
}
