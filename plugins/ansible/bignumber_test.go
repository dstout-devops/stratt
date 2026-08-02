package ansible

import (
	"encoding/json"
	"strings"
	"testing"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

// rsaModulusEvent is a REAL runner_on_ok line, captured verbatim from
// community.crypto.openssl_privatekey running under ansible-runner in the EE image.
// The `modulus` is a 617-digit integer — an ordinary RSA-2048 public modulus.
//
// It is a literal here on purpose: the defect it pins is not something a hand-written
// fixture would suggest, because the line is perfectly valid JSON. Only decoding it
// into Go's map[string]any (where every number becomes float64) rejects it.
const rsaModulusEvent = `{"uuid":"82b891fc-dee5-4136-b598-743281b98383","counter":6,` +
	`"stdout":"changed: [cn]","start_line":5,"end_line":6,"event":"runner_on_ok","pid":15,` +
	`"event_data":{"host":"cn","task":"private key","res":{"changed":true,"type":"RSA","size":2048,` +
	`"filename":"/tmp/tls/app.key","public_data":{"size":2048,"exponent":65537,"modulus":` +
	`28152346515117827434823726840049882397202560551012496525309543903665129633749571983621101868998164123476` +
	`05994677074073318915697752379859774843031970205778268609760433180314650006393068067234606888795331287170` +
	`73284670138758992669178058017402291226301553081717543586524262098771855371228407024971919007491951765317` +
	`84798803409682248968216857983620312632789824708429163598971250183506098093021049640623453649503452990419` +
	`55329118044830683738893148457879910891299389878718319726657854903516217134412312541462787622315213368223` +
	`0656910043038507677625313542904293517700302721013337441125590787893064825640215148119362870324753}}}}`

// TestParseEvent_SurvivesHugeIntegers pins the decoder defect that made the certificate
// path unreportable. Before the UseNumber fix, encoding/json rejected this entire event
// with "cannot unmarshal number … into Go value of type float64", so a successful
// per-host result degraded into an untyped diagnostic: no ItemResult, no facts, no
// drift. Any module returning a big integer (every community.crypto key/cert module,
// and plenty of others) hit it.
func TestParseEvent_SurvivesHugeIntegers(t *testing.T) {
	ev, ok := parseEvent([]byte(rsaModulusEvent))
	if !ok {
		t.Fatal("a valid runner_on_ok carrying an RSA modulus must PARSE — an unparsed event loses its ItemResult, facts and drift (§1.8)")
	}
	if ev.Event != "runner_on_ok" {
		t.Fatalf("event = %q, want runner_on_ok", ev.Event)
	}
	host, status := hostStatus(ev)
	if host != "cn" || status != pluginv1.ItemResult_STATUS_CHANGED {
		t.Fatalf("hostStatus = (%q, %v), want (cn, CHANGED)", host, status)
	}
}

// TestParseEvent_KeepsBigIntegersExact: json.Number must preserve the literal. Decoding
// a 617-digit modulus through float64 would silently corrupt it, so any fact or drift
// fragment carrying one has to round-trip byte-exactly, not approximately.
func TestParseEvent_KeepsBigIntegersExact(t *testing.T) {
	ev, ok := parseEvent([]byte(rsaModulusEvent))
	if !ok {
		t.Fatal("parseEvent failed")
	}
	res := ev.EventData["res"].(map[string]any)
	pub := res["public_data"].(map[string]any)
	mod, isNumber := pub["modulus"].(json.Number)
	if !isNumber {
		t.Fatalf("modulus decoded as %T, want json.Number (float64 would lose precision)", pub["modulus"])
	}
	if len(mod.String()) != 617 {
		t.Fatalf("modulus has %d digits, want the exact 617 captured from the wire", len(mod.String()))
	}
	if strings.ContainsAny(mod.String(), "e+.") {
		t.Fatalf("modulus was normalized to floating point (%s) — precision lost", mod.String())
	}
}

// TestEventShaped_SeparatesDefectsFromBanners: a misparse must be distinguishable from
// ordinary output. Runner banners and python tracebacks are legitimately non-JSON and
// ride the MF5 diagnostic channel; a line that IS an event but failed to decode is a
// DEFECT losing typed signal. Conflating them is what let the overflow above go
// unnoticed, so the classification is tested directly.
func TestEventShaped_SeparatesDefectsFromBanners(t *testing.T) {
	shaped := map[string]string{
		"big-number event":  rsaModulusEvent,
		"plain event":       `{"event":"runner_on_ok","event_data":{"host":"h"}}`,
		"unknown event key": `{"event":"some_future_event","weird":{"deeply":[1,2]}}`,
	}
	for name, line := range shaped {
		if !eventShaped([]byte(line)) {
			t.Errorf("%s: must be classified as an EVENT (a decode failure here is a defect)", name)
		}
	}
	notShaped := map[string]string{
		"runner banner":  `Identity added: /runner/artifacts/ssh_key_data`,
		"traceback":      `Traceback (most recent call last):`,
		"empty":          ``,
		"json no event":  `{"counter":1,"stdout":"hello"}`,
		"json empty ev":  `{"event":"","counter":1}`,
		"malformed json": `{"event":"runner_on_ok"`,
	}
	for name, line := range notShaped {
		if eventShaped([]byte(line)) {
			t.Errorf("%s: must NOT be classified as an event — it belongs on the ordinary diagnostic channel (MF5)", name)
		}
	}
}

// TestShim_CryptoPlayIsNotReportedVacuous is the end-to-end regression, and the reason
// this fix blocks the content slice: a one-task play that SUCCESSFULLY created an RSA
// key emitted zero ItemResults (the event was unparseable), so the vacuous-run guard
// concluded nothing was actuated and reported the successful run as FAILED — with the
// wrong explanation. Verified live against the EE image before the fix.
func TestShim_CryptoPlayIsNotReportedVacuous(t *testing.T) {
	req := Request{
		Params:  json.RawMessage(`{"play":"- hosts: all"}`),
		Targets: []Target{{Name: "cn", Address: "local"}},
	}
	out := runShim(t, req, fakeRunner{rc: 0, lines: []string{rsaModulusEvent}})

	var terminal *pluginv1.TaskEvent
	results := 0
	for _, r := range out {
		if ev := r.GetEvent(); ev != nil && ev.GetTerminal() {
			terminal = ev
		}
		if res := r.GetResult(); res != nil {
			results++
			if res.GetItemKey() != "cn" || res.GetStatus() != pluginv1.ItemResult_STATUS_CHANGED {
				t.Errorf("result = (%q, %v), want (cn, CHANGED)", res.GetItemKey(), res.GetStatus())
			}
		}
	}
	if results != 1 {
		t.Fatalf("got %d ItemResults, want exactly 1 — the crypto task's per-host result must reach the hub", results)
	}
	if terminal == nil {
		t.Fatal("no terminal emitted (MF5)")
	}
	if !terminal.GetOk() {
		t.Fatalf("a play that actually created the key must terminate OK, got %q", terminal.GetMessage())
	}
}

// TestShim_UnparsedEventIsVisibleAndOwnsTheDiagnosis covers the case where a runner
// event is STILL undecodable for some future type-shape reason. Two things must hold:
// the line surfaces at WARN under its own kind (so it reads as a defect, not as noise),
// and the vacuous-run verdict says "I could not decode events" rather than blaming the
// play's `hosts:` pattern — a cause it has no evidence for (§1.8).
func TestShim_UnparsedEventIsVisibleAndOwnsTheDiagnosis(t *testing.T) {
	// A shaped event whose event_data is a STRING, not an object — decodes into the
	// probe (which reads only `event`) but never into RunnerEvent.
	broken := `{"event":"runner_on_ok","event_data":"not-an-object"}`
	if _, ok := parseEvent([]byte(broken)); ok {
		t.Fatal("fixture must not parse, or the test proves nothing")
	}
	req := Request{
		Params:  withDeclaredSSH(t, `{"play":"- hosts: all"}`),
		Targets: []Target{{Name: "web-01", Address: "10.0.0.1"}},
	}
	out := runShim(t, req, fakeRunner{rc: 0, lines: []string{broken}})

	sawWarnedDefect := false
	var terminal *pluginv1.TaskEvent
	for _, r := range out {
		ev := r.GetEvent()
		if ev == nil {
			continue
		}
		if ev.GetTerminal() {
			terminal = ev
		}
		if ev.GetFields()["kind"] == "unparsed-event" && ev.GetLevel() == pluginv1.TaskEvent_LEVEL_WARN {
			sawWarnedDefect = true
		}
	}
	if !sawWarnedDefect {
		t.Error("an undecodable EVENT must surface at WARN with kind=unparsed-event, not as an INFO diagnostic")
	}
	if terminal == nil || terminal.GetOk() {
		t.Fatal("a run whose events could not be decoded must not report success — actuation is unknown")
	}
	if !strings.Contains(terminal.GetMessage(), "could not be DECODED") {
		t.Errorf("terminal must name the decode failure as the cause, got %q", terminal.GetMessage())
	}
	if strings.Contains(terminal.GetMessage(), "no hosts matched") {
		t.Errorf("terminal must NOT blame the play's pattern — that cause was never observed: %q", terminal.GetMessage())
	}
}

// TestVacuousRun_UnparsedEventsOutrankOtherCauses pins the precedence directly: even
// when ansible DID report no-hosts-matched, an undecodable event means actuation is
// unknown, and the honest diagnosis is the one the shim can actually support.
func TestVacuousRun_UnparsedEventsOutrankOtherCauses(t *testing.T) {
	tgt := []Target{{Name: "web-01"}}
	got := vacuousRun(0, tgt, 0, "", true, 2)
	if !strings.Contains(got, "2 ansible event(s) could not be DECODED") {
		t.Fatalf("decode failure must own the diagnosis: %q", got)
	}
	if strings.Contains(got, "no hosts matched") {
		t.Fatalf("must not also assert the pattern cause: %q", got)
	}
}

// TestParseEvent_RejectsTrailingData guards the regression the UseNumber switch itself
// introduced. json.Unmarshal rejects trailing content; a json.Decoder does not — it stops
// after the first value. So moving to a Decoder silently made a TORN or CONCATENATED line
// parse as just its first event, dropping the rest with no ItemResult and no diagnostic:
// the exact invisibility the decoder fix exists to abolish, reintroduced by the fix.
func TestParseEvent_RejectsTrailingData(t *testing.T) {
	ok1 := `{"event":"runner_on_ok","event_data":{"host":"h"}}`
	ok2 := `{"event":"runner_on_failed","event_data":{"host":"h"}}`

	if _, ok := parseEvent([]byte(ok1)); !ok {
		t.Fatal("a single well-formed event must parse")
	}
	if _, ok := parseEvent([]byte(ok1 + "  \t\n")); !ok {
		t.Error("trailing whitespace is not trailing DATA and must still parse")
	}
	// Two events on one line: taking the first and discarding the second would lose a
	// FAILURE here — the worst possible thing to drop silently.
	if _, ok := parseEvent([]byte(ok1 + ok2)); ok {
		t.Error("a concatenated line must be REJECTED, not silently truncated to its first event")
	}
	if _, ok := parseEvent([]byte(ok1 + `garbage`)); ok {
		t.Error("a line with trailing garbage must be rejected")
	}
	// Rejected, but still recognizably an EVENT — so it reaches the operator at WARN as
	// kind=unparsed-event rather than blending into ordinary banner output.
	if !eventShaped([]byte(ok1 + ok2)) {
		t.Error("a concatenated event line must still classify as event-shaped so it surfaces as a defect")
	}
}
