package awxfacade

import (
	"os"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/desiredstate"
)

// See api's TestMain for the full reasoning. Since ADR-0138 D3/D4 a plugin's SELF contracts —
// including `actuators/ansible.input` — live in the plugin's own tree and reach the registry
// through the estate parse. The façade validates a launch against that registry, so a test process
// that never parses an estate sees `ansible` as uncontracted.
//
// Production ordering is already correct: strattd's boot-time parse runs on every replica before
// anything serves. This mirrors it.
func TestMain(m *testing.M) {
	if _, err := desiredstate.ParseDir("../../../estate", nil); err != nil {
		println("awxfacade tests: reference estate not parsed:", err.Error())
	}
	os.Exit(m.Run())
}
