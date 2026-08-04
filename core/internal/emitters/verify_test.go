package emitters

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// ── ADR-0164 D2 · a source signs, and the core does not hold the key ──────────────────────────

// recordingVerifier stands in for the provider AND records exactly what crossed the port, so the
// §2.5 property can be asserted against the request rather than against the intention.
type recordingVerifier struct {
	gotBody []byte
	gotSig  []byte
	gotSpec types.VerifySpec
	valid   bool
	err     error
}

func (v *recordingVerifier) Verify(_ context.Context, spec types.VerifySpec, body, sig []byte) (bool, error) {
	v.gotBody, v.gotSig, v.gotSpec = body, sig, spec
	return v.valid, v.err
}

// A body whose re-serialization is provably different from its own bytes: odd whitespace, key
// order that json.Marshal would sort, and a number Go would reformat. If anything between the
// socket and the verifier parses and re-emits this, the MAC breaks.
const wobblyBody = `{ "z":1,  "a":  2,
  "n": 1.500, "s":"x" }`

func signedEmitter() types.Emitter {
	return types.Emitter{
		Name: "github", Kind: types.EmitterWebhook, TokenHash: hashOf(demoToken),
		Verify: &types.VerifySpec{
			Header: "X-Hub-Signature-256", Algorithm: "hmac-sha256",
			Encoding: types.SignatureHex, Prefix: "sha256=", KeyRef: "gh-webhook",
		},
	}
}

func postSigned(em types.Emitter, v *recordingVerifier, sigHeader, sigValue, body string) int {
	in := &Ingest{log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		store: stubStore{em: em}, bus: stubBus{}}
	if v != nil {
		in.Verifier = v
	}
	req := httptest.NewRequest(http.MethodPost, "/emitters/"+em.Name, bytes.NewReader([]byte(body)))
	req.Header.Set(types.DefaultTokenHeader, demoToken)
	if sigHeader != "" {
		req.Header.Set(sigHeader, sigValue)
	}
	rec := httptest.NewRecorder()
	in.Handler().ServeHTTP(rec, req)
	return rec.Code
}

// THE PROPERTY THE WHOLE DESIGN EXISTS FOR (§2.5, ADR-0052): what crosses the port is a key
// COORDINATE. If material ever appeared in this request, the delegation would be theatre.
func TestTheCoreSendsCoordinatesAndNeverMaterial(t *testing.T) {
	v := &recordingVerifier{valid: true}
	sig := "sha256=" + hex.EncodeToString([]byte("whatever"))
	if code := postSigned(signedEmitter(), v, "X-Hub-Signature-256", sig, wobblyBody); code != http.StatusAccepted {
		t.Fatalf("a valid signature must be accepted: HTTP %d", code)
	}
	if v.gotSpec.KeyRef != "gh-webhook" {
		t.Errorf("the key coordinate must travel: %+v", v.gotSpec)
	}
	// The declaration carries a reference and an algorithm — there is no field for material and
	// nothing resembling a secret in what was sent.
	if strings.Contains(v.gotSpec.KeyRef, demoToken) {
		t.Error("the key coordinate must not be a secret")
	}
	if v.gotSpec.Algorithm != "hmac-sha256" {
		t.Errorf("the declared algorithm must travel, never be inferred: %q", v.gotSpec.Algorithm)
	}
}

// D3: THE BYTES VERIFIED ARE THE BYTES RECEIVED. A MAC covers exactly what the source sent, so a
// payload that got parsed and re-emitted anywhere on the way would fail on whitespace, key order
// and number formatting — intermittently, which is worse than failing.
func TestTheVerifiedBytesAreTheReceivedBytes(t *testing.T) {
	v := &recordingVerifier{valid: true}
	postSigned(signedEmitter(), v, "X-Hub-Signature-256", "sha256=00", wobblyBody)
	if string(v.gotBody) != wobblyBody {
		t.Fatalf("the verifier saw different bytes than the source sent:\n got %q\nwant %q",
			v.gotBody, wobblyBody)
	}
}

// The presented signature reaches the provider as raw MAC BYTES — prefix stripped, encoding
// decoded — so the provider compares bytes rather than spellings.
func TestTheSignatureIsDecodedBeforeItTravels(t *testing.T) {
	v := &recordingVerifier{valid: true}
	postSigned(signedEmitter(), v, "X-Hub-Signature-256", "sha256=deadbeef", wobblyBody)
	if hex.EncodeToString(v.gotSig) != "deadbeef" {
		t.Fatalf("signature bytes = %x, want deadbeef", v.gotSig)
	}
}

// Every way this can fail must REFUSE, and none of them may fall through to unverified ingest.
func TestEveryVerificationFailureRefuses(t *testing.T) {
	cases := []struct {
		name   string
		v      *recordingVerifier
		header string
		value  string
	}{
		{"answered no", &recordingVerifier{valid: false}, "X-Hub-Signature-256", "sha256=deadbeef"},
		{"provider unreachable", &recordingVerifier{err: errors.New("down")}, "X-Hub-Signature-256", "sha256=deadbeef"},
		{"no signature header", &recordingVerifier{valid: true}, "", ""},
		{"missing declared prefix", &recordingVerifier{valid: true}, "X-Hub-Signature-256", "deadbeef"},
		{"not the declared encoding", &recordingVerifier{valid: true}, "X-Hub-Signature-256", "sha256=not-hex"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if code := postSigned(signedEmitter(), c.v, c.header, c.value, wobblyBody); code != http.StatusUnauthorized {
				t.Fatalf("HTTP %d, want 401", code)
			}
		})
	}
}

// NO PROVIDER BOUND MUST NOT MEAN NO CHECK. An estate that declared a signature and got none
// verified would be the worst of both: it believes it is authenticated and it is not.
func TestASignedEmitterWithNoProviderBoundRefuses(t *testing.T) {
	if code := postSigned(signedEmitter(), nil, "X-Hub-Signature-256", "sha256=deadbeef", wobblyBody); code != http.StatusUnauthorized {
		t.Fatalf("HTTP %d, want 401 — a declared signature with no verifier must refuse, not degrade", code)
	}
}

// THE REGRESSION THAT MATTERS: an Emitter declaring no signature never touches the port, so the
// ingest path stays plugin-free wherever a signature is not declared (D4).
func TestAnUnsignedEmitterNeverReachesTheVerifier(t *testing.T) {
	v := &recordingVerifier{err: errors.New("must not be called")}
	em := types.Emitter{Name: "hooks", Kind: types.EmitterWebhook, TokenHash: hashOf(demoToken)}
	if code := postSigned(em, v, "", "", wobblyBody); code != http.StatusAccepted {
		t.Fatalf("an unsigned Emitter must ingest as before: HTTP %d", code)
	}
	if v.gotBody != nil {
		t.Error("an Emitter declaring no signature must not consult a verifier at all")
	}
}
