package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/dstout-devops/stratt/types"
)

func TestRegisterMCPContractBlocksDrift(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	name := "mcp/demo-tools/greet.input"
	schemaA := []byte(`{"type":"object"}`)
	schemaB := []byte(`{"type":"object","required":["name"]}`)

	if err := store.RegisterMCPContract(ctx, name, 1, "hashA", schemaA); err != nil {
		t.Fatalf("first pin: %v", err)
	}
	// Same rev, same hash: noop.
	if err := store.RegisterMCPContract(ctx, name, 1, "hashA", schemaA); err != nil {
		t.Fatalf("idempotent re-pin: %v", err)
	}
	// Same rev, different hash: BLOCKING (§1.5 rung 3) — never auto-version.
	err := store.RegisterMCPContract(ctx, name, 1, "hashB", schemaB)
	if !errors.Is(err, ErrContractDrift) {
		t.Fatalf("same-rev drift must block: %v", err)
	}
	// Accepting the change is a Git act: bump rev.
	if err := store.RegisterMCPContract(ctx, name, 2, "hashB", schemaB); err != nil {
		t.Fatalf("rev bump: %v", err)
	}

	c, err := store.GetContract(ctx, name, 2)
	if err != nil || c.Hash != "hashB" || c.Rung != types.RungMCPDeclared {
		t.Fatalf("get pinned: %v %+v", err, c)
	}
	if _, err := store.GetContract(ctx, name, 9); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing pin must be not-found: %v", err)
	}
}
