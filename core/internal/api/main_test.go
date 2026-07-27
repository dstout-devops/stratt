package api

import (
	"os"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/desiredstate"
)

// TestMain parses the reference estate once, and the reason is the point rather than a setup
// detail: since ADR-0138 D3/D4, a plugin's SELF contracts live in its own tree and reach the
// contract registry through the estate parse, not through the binary's embedded FS.
//
// The API door validates a Workflow's Steps against that registry (workflowFromWire →
// ValidateWorkflow → contract.Get). In production the ordering is already right — strattd's
// boot-time estate parse runs on every replica and BEFORE the API handler is built — but a unit
// test that never parses an estate has an empty plugin-contract set, and `action: helm/deploy`
// then reads as uncontracted.
//
// Doing this here rather than swapping the fixtures for core-shipped Actions is deliberate: the
// tests keep exercising the Actions the estate actually ships, and the setup mirrors the boot
// sequence instead of hiding the dependency on it.
func TestMain(m *testing.M) {
	if _, err := desiredstate.ParseDir("../../../estate", nil); err != nil {
		// Not fatal: a tree without the reference estate (a trimmed checkout) should still run
		// the tests that need no plugin contract, and the ones that do will fail loudly and say why.
		println("api tests: reference estate not parsed:", err.Error())
	}
	os.Exit(m.Run())
}
