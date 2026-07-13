package graph

import (
	"context"
	"crypto/md5" //nolint:gosec // non-cryptographic id hash, mirrors awxfacade.awxID
	"encoding/binary"
	"errors"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

// goAWXID recomputes the façade's awxID (crypto/md5 → first 4 bytes BE → int31)
// so this test proves the SQL twin graph.awx_run_id matches Go byte-for-byte.
func goAWXID(s string) int64 {
	sum := md5.Sum([]byte(s)) //nolint:gosec
	return int64(binary.BigEndian.Uint32(sum[:4]) & 0x7fffffff)
}

func TestGetRunByAWXID(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	run, err := s.CreateRun(ctx, types.Run{WorkflowID: "wf-awx", ViewRef: "view://all", ViewVersion: 1})
	if err != nil {
		t.Fatal(err)
	}

	// The SQL function must resolve the Run from the id Go would synthesize.
	got, err := s.GetRunByAWXID(ctx, goAWXID(run.ID))
	if err != nil {
		t.Fatalf("GetRunByAWXID(goAWXID(%s)): %v — Go/SQL hash parity broken", run.ID, err)
	}
	if got.ID != run.ID {
		t.Fatalf("resolved run %s, want %s", got.ID, run.ID)
	}

	// An unknown id is a clean ErrNotFound (not a 500).
	if _, err := s.GetRunByAWXID(ctx, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown awx id must be ErrNotFound, got %v", err)
	}
}
