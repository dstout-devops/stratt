package emitters

import (
	"encoding/hex"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/dstout-devops/stratt/types"
)

// ── ADR-0167 · a replay is a valid signature at the wrong time ────────────────────────────────

func stripeEmitter(tolerance int) types.Emitter {
	return types.Emitter{
		Name: "stripe", Kind: types.EmitterWebhook, TokenHash: hashOf(demoToken),
		Verify: &types.VerifySpec{
			Header: "Stripe-Signature", Algorithm: "hmac-sha256", KeyRef: "stripe-webhook",
			Format: types.SignatureFormatKV, SignatureKey: "v1", TimestampKey: "t",
			SignedPayload: types.SignedPayloadTimestampBody, ToleranceSeconds: tolerance,
		},
	}
}

func stripeHeader(ts time.Time) string {
	return "t=" + strconv.FormatInt(ts.Unix(), 10) + ",v1=" + hex.EncodeToString([]byte("sig"))
}

// A scheme core has never seen, expressed entirely as data: the kv split, which pair is the MAC,
// which is the clock, and what the MAC covers.
func TestATimestampedSchemeIsParsedFromTheDeclaration(t *testing.T) {
	v := &recordingVerifier{valid: true}
	now := time.Now()
	if code := postSigned(stripeEmitter(300), v, "Stripe-Signature", stripeHeader(now), wobblyBody); code != http.StatusAccepted {
		t.Fatalf("a fresh, correctly shaped request must be accepted: HTTP %d", code)
	}
	if hex.EncodeToString(v.gotSig) != hex.EncodeToString([]byte("sig")) {
		t.Errorf("the v1 pair must be the signature: %x", v.gotSig)
	}
	// signedPayload: timestamp.body — and the body inside it is untouched (ADR-0164 D3 holds).
	want := strconv.FormatInt(now.Unix(), 10) + "." + wobblyBody
	if string(v.gotBody) != want {
		t.Fatalf("the MAC must cover <t>.<body> byte for byte:\n got %q\nwant %q", v.gotBody, want)
	}
}

// THE CONTROL THIS ADR EXISTS FOR. A verifier that fails the test if consulted proves the refusal
// happens BEFORE the KMS is asked (D2) — cheapest check first, least work leaked to a replayer.
func TestAStaleTimestampIsRefusedBeforeTheVerifierIsConsulted(t *testing.T) {
	v := &recordingVerifier{valid: true}
	old := time.Now().Add(-30 * time.Minute)
	if code := postSigned(stripeEmitter(300), v, "Stripe-Signature", stripeHeader(old), wobblyBody); code != http.StatusUnauthorized {
		t.Fatalf("a replayed request must be refused: HTTP %d", code)
	}
	if v.gotBody != nil {
		t.Error("the verifier was consulted for a request already known to be stale")
	}
}

// Skew cuts both ways: only refusing the past is a half-check, because an attacker who can set the
// clock forward would otherwise mint requests that stay valid indefinitely.
func TestAFutureTimestampIsRefusedToo(t *testing.T) {
	v := &recordingVerifier{valid: true}
	future := time.Now().Add(30 * time.Minute)
	if code := postSigned(stripeEmitter(300), v, "Stripe-Signature", stripeHeader(future), wobblyBody); code != http.StatusUnauthorized {
		t.Fatalf("a far-future timestamp must be refused: HTTP %d", code)
	}
	// …and a small skew inside tolerance still passes, or the control is unusable in practice.
	if code := postSigned(stripeEmitter(300), v, "Stripe-Signature", stripeHeader(time.Now().Add(10*time.Second)), wobblyBody); code != http.StatusAccepted {
		t.Errorf("modest skew inside tolerance must be accepted: HTTP %d", code)
	}
}

// A header missing a declared pair is malformed, not "absent timestamp, carry on".
func TestAMissingDeclaredPairIsRefused(t *testing.T) {
	v := &recordingVerifier{valid: true}
	for _, hdr := range []string{"v1=" + hex.EncodeToString([]byte("sig")), "t=123"} {
		if code := postSigned(stripeEmitter(300), v, "Stripe-Signature", hdr, wobblyBody); code != http.StatusUnauthorized {
			t.Errorf("header %q must be refused: HTTP %d", hdr, code)
		}
	}
}

// THE REGRESSION THAT MATTERS: every signed Emitter that exists declares no timestamp, and must
// behave exactly as ADR-0164 shipped it — MAC over the body alone, no freshness check.
func TestASignedEmitterWithNoTimestampIsUnchanged(t *testing.T) {
	v := &recordingVerifier{valid: true}
	if code := postSigned(signedEmitter(), v, "X-Hub-Signature-256", "sha256=deadbeef", wobblyBody); code != http.StatusAccepted {
		t.Fatalf("HTTP %d", code)
	}
	if string(v.gotBody) != wobblyBody {
		t.Fatalf("the MAC must still cover the body alone: %q", v.gotBody)
	}
}
