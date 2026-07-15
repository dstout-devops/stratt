package desiredstate

import (
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// TestValidateEmitterStream proves the token-less stream Emitter kind (ADR-0039):
// stream requires an EMPTY tokenHash; receive kinds still require a 64-hex hash.
func TestValidateEmitterStream(t *testing.T) {
	hash := strings.Repeat("a", 64)
	cases := []struct {
		name string
		e    types.Emitter
		ok   bool
	}{
		{"stream no token", types.Emitter{Name: "salt", Kind: types.EmitterStream}, true},
		{"stream with token rejected", types.Emitter{Name: "salt", Kind: types.EmitterStream, TokenHash: hash}, false},
		{"webhook needs token", types.Emitter{Name: "h", Kind: types.EmitterWebhook}, false},
		{"webhook with token", types.Emitter{Name: "h", Kind: types.EmitterWebhook, TokenHash: hash}, true},
		{"unknown kind", types.Emitter{Name: "x", Kind: "poller"}, false},
		{"no name", types.Emitter{Kind: types.EmitterStream}, false},
	}
	for _, c := range cases {
		err := ValidateEmitter(c.e)
		if (err == nil) != c.ok {
			t.Fatalf("%s: ok=%v err=%v", c.name, c.ok, err)
		}
	}
}
