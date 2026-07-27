package notify

import (
	"os"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/desiredstate"
)

// See api's TestMain. Since ADR-0138 D3/D4 the notify plugin's SELF contracts —
// `actions/notify/{webhook,smtp}.{input,output}` — live in its own tree and reach the registry
// through the estate parse, not the binary's embedded FS. A Sink's delivery params are validated
// against those, so a test process that never parses an estate sees them as uncontracted.
func TestMain(m *testing.M) {
	if _, err := desiredstate.ParseDir("../../../estate", nil); err != nil {
		println("notify tests: reference estate not parsed:", err.Error())
	}
	os.Exit(m.Run())
}
