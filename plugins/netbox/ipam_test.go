package netbox

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A stateful fake NetBox: the description-keyed lookup returns nothing until a prefix is allocated,
// then returns it — modeling NetBox as the sole allocation record (ADR-0111 D4/F1).
func allocFakeNetBox(t *testing.T, posts *int) *httptest.Server {
	t.Helper()
	allocated := false
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux := http.NewServeMux()
	// POST available-prefixes: allocate the child (parent id 1 in this fixture).
	mux.HandleFunc("/api/ipam/prefixes/1/available-prefixes/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		*posts++
		allocated = true
		writeJSON(w, map[string]any{"id": 99, "prefix": "10.30.4.0/24"})
	})
	// GET list: idempotency lookup (by description) + parent lookup (by prefix).
	mux.HandleFunc("/api/ipam/prefixes/", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("description") != "":
			if allocated {
				writeJSON(w, map[string]any{"results": []map[string]any{{"id": 99, "prefix": "10.30.4.0/24"}}})
			} else {
				writeJSON(w, map[string]any{"results": []any{}})
			}
		case q.Get("prefix") != "":
			writeJSON(w, map[string]any{"results": []map[string]any{{"id": 1, "prefix": q.Get("prefix")}}})
		default:
			writeJSON(w, map[string]any{"results": []any{}})
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestServer(endpoint string) *Server {
	return NewServer(Config{Endpoint: endpoint, Token: "t"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// A first resolve allocates a prefix; a second resolve with the SAME key returns the SAME CIDR and
// performs NO second allocation — idempotency anchored in NetBox (ADR-0111 D4/F1).
func TestAllocateIPAM_IdempotentByKey(t *testing.T) {
	var posts int
	srv := allocFakeNetBox(t, &posts)
	s := newTestServer(srv.URL)
	req := ipamRequest{Key: "app-subnet", Pool: "10.30.0.0/16", Size: 24, Region: "eu-west"}

	out1, err := s.allocateIPAM(context.Background(), req)
	if err != nil {
		t.Fatalf("first allocate: %v", err)
	}
	if out1["cidr"] != "10.30.4.0/24" {
		t.Fatalf("cidr = %v, want 10.30.4.0/24", out1["cidr"])
	}

	out2, err := s.allocateIPAM(context.Background(), req)
	if err != nil {
		t.Fatalf("second allocate: %v", err)
	}
	if out2["cidr"] != "10.30.4.0/24" {
		t.Fatalf("second cidr = %v, want the same 10.30.4.0/24", out2["cidr"])
	}
	if posts != 1 {
		t.Fatalf("expected exactly ONE allocation POST across two resolves (idempotency), got %d", posts)
	}
}

func TestIPAMRequestValidate(t *testing.T) {
	cases := []struct {
		name string
		req  ipamRequest
		ok   bool
	}{
		{"valid pool", ipamRequest{Key: "k", Pool: "10.0.0.0/8", Size: 24}, true},
		{"valid role", ipamRequest{Key: "k", Role: "app", Size: 24}, true},
		{"missing key", ipamRequest{Pool: "10.0.0.0/8", Size: 24}, false},
		{"missing size", ipamRequest{Key: "k", Pool: "10.0.0.0/8"}, false},
		{"pool and role", ipamRequest{Key: "k", Pool: "10.0.0.0/8", Role: "app", Size: 24}, false},
		{"neither pool nor role", ipamRequest{Key: "k", Size: 24}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.validate()
			if tc.ok && err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("expected invalid")
			}
		})
	}
}
