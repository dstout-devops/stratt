package siterelay

import (
	"context"
	"io"
	"strings"
	"testing"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// ── ADR-0166 · a key custodian does not travel ────────────────────────────────────────────────

// The three custody verbs refuse WITHOUT dialling — a nil dialer proves it, because any attempt to
// reach the wire would panic rather than return. The call is wrong before it becomes a request.
func TestCustodyVerbsRefuseWithoutDialling(t *testing.T) {
	c := &Client{} // no dial func: touching the transport would panic
	ctx := context.Background()

	cases := []struct {
		verb string
		call func() error
	}{
		{"WrapKey", func() error { _, err := c.WrapKey(ctx, &pluginv1.WrapKeyRequest{}); return err }},
		{"UnwrapKey", func() error { _, err := c.UnwrapKey(ctx, &pluginv1.UnwrapKeyRequest{}); return err }},
		{"VerifyMAC", func() error { _, err := c.VerifyMAC(ctx, &pluginv1.VerifyMACRequest{}); return err }},
	}
	for _, tc := range cases {
		t.Run(tc.verb, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("must refuse")
			}
			// The message has to carry the REASON, not just the refusal: "unknown method" is the
			// failure this replaced, and it reads like version skew (§1.8).
			for _, want := range []string{tc.verb, "ADR-0166", "does not travel"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("missing %q in: %v", want, err)
				}
			}
			if !strings.Contains(err.Error(), "DEK") {
				t.Errorf("the refusal must say what would cross the link: %v", err)
			}
		})
	}
}

// fakeStream feeds serveCall one opening request and captures what comes back.
type fakeStream struct {
	in   []Msg
	sent []Msg
}

func (f *fakeStream) Send(m Msg) error { f.sent = append(f.sent, m); return nil }
func (f *fakeStream) Recv() (Msg, error) {
	if len(f.in) == 0 {
		return Msg{}, io.EOF
	}
	m := f.in[0]
	f.in = f.in[1:]
	return m, nil
}
func (f *fakeStream) Close() error { return nil }

func serve(method string) []Msg {
	fs := &fakeStream{in: []Msg{{Method: method}}}
	serveCall(context.Background(), fs, nil) // nil plugin: a served verb would panic, an unserved one must not
	return fs.sent
}

// The far end names these verbs rather than letting them fall into "unknown method", which reads
// like version skew and sends the next engineer looking for a missing case (ADR-0166 D1).
func TestTheRelayServerRefusesCustodyByName(t *testing.T) {
	for _, verb := range []string{mWrapKey, mUnwrapKey, mVerifyMAC} {
		t.Run(verb, func(t *testing.T) {
			sent := serve(verb)
			if len(sent) == 0 || !sent[len(sent)-1].Terminal {
				t.Fatalf("expected a terminal refusal, got %+v", sent)
			}
			err := sent[len(sent)-1].Err
			if strings.Contains(err, "unknown method") {
				t.Fatalf("refused as an unknown method — the reason is lost: %s", err)
			}
			for _, want := range []string{verb, "ADR-0166", "does not travel"} {
				if !strings.Contains(err, want) {
					t.Errorf("missing %q in: %s", want, err)
				}
			}
		})
	}
}

// THE REGRESSION THAT MATTERS: this change edits the switch every dispatched Step goes through, so
// a verb that DOES travel must still reach the plugin. A nil plugin makes that observable — a
// served verb panics on the call, an unserved one returns a refusal without ever touching it.
func TestTheVerbsThatTravelStillReachThePlugin(t *testing.T) {
	for _, verb := range []string{mGetManifest, mHealth, mPlan, mObserve, mApply, mDestroy, mInvoke, mSubscribe} {
		t.Run(verb, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("%s did not reach the plugin — it was refused or dropped by the switch", verb)
				}
			}()
			serve(verb)
		})
	}
}
