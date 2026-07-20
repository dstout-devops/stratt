package adopt

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// fakeCatalog stands in for the projection graph: an identity present in the map is "observed"
// and maps to its native object id — exactly what the graph catalog resolves. adopt's core half
// is tool-blind, so the whole test needs only this (no AWX, no deep-read, no transform).
type fakeCatalog struct {
	native map[string]int
	source string
	live   []string
}

func (f fakeCatalog) Resolve(_ context.Context, _, identity string) (int, string, bool, error) {
	id, ok := f.native[identity]
	return id, f.source, ok, nil
}

func (f fakeCatalog) LiveExecutions(_ context.Context, _, _ string) ([]string, error) {
	return f.live, nil
}

// TestResolveObservedObject: an observed object resolves to the coordinates the plugin Action
// needs — native id, source, and the pre-resolved live-execution set for the cutover guard.
func TestResolveObservedObject(t *testing.T) {
	cat := fakeCatalog{native: map[string]int{"ctrl-a/10": 10}, source: "ctrl-a", live: []string{"Nightly Deploy"}}
	got, err := Resolve(context.Background(), cat, Request{Kind: KindTemplate, Identity: "ctrl-a/10"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := Resolved{NativeID: 10, Source: "ctrl-a", Live: []string{"Nightly Deploy"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved = %+v, want %+v", got, want)
	}
}

// TestResolveRejectsUnobserved: an identity not in the catalog fails loud — nothing to adopt.
func TestResolveRejectsUnobserved(t *testing.T) {
	cat := fakeCatalog{native: map[string]int{}, source: "ctrl-a"}
	_, err := Resolve(context.Background(), cat, Request{Kind: KindTemplate, Identity: "ctrl-a/999"})
	if !errors.Is(err, ErrNotObserved) {
		t.Fatalf("expected ErrNotObserved, got %v", err)
	}
}

// TestResolveRejectsUnsupportedKind: adopt resolves only the kinds it can transform.
func TestResolveRejectsUnsupportedKind(t *testing.T) {
	cat := fakeCatalog{native: map[string]int{"ctrl-a/1": 1}, source: "ctrl-a"}
	_, err := Resolve(context.Background(), cat, Request{Kind: "ansible.org", Identity: "ctrl-a/1"})
	if !errors.Is(err, ErrUnsupportedKind) {
		t.Fatalf("expected ErrUnsupportedKind, got %v", err)
	}
}
