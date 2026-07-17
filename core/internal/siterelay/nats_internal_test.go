package siterelay

import (
	"bytes"
	"testing"
)

// TestFrameRoundTrip proves the on-wire framing preserves the opaque Msg + seq
// (pure — no NATS). The relay marshals framing only; Payload stays verbatim.
func TestFrameRoundTrip(t *testing.T) {
	in := Msg{Method: "Apply", Payload: []byte{0x00, 0x01, 0xff, 0x7f}, Terminal: true, Err: "boom", Cancel: false}
	b, err := encodeFrame(7, in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	seq, out, err := decodeFrame(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if seq != 7 {
		t.Fatalf("seq lost: %d", seq)
	}
	if out.Method != in.Method || out.Terminal != in.Terminal || out.Err != in.Err || !bytes.Equal(out.Payload, in.Payload) {
		t.Fatalf("frame round-trip mismatch: %+v vs %+v", out, in)
	}
	if _, _, err := decodeFrame([]byte("not json")); err == nil {
		t.Fatal("garbage frame must fail to decode")
	}
}
