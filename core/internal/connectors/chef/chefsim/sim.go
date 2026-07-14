// Package chefsim is a dev-harness stand-in for the Chef Infra Server node API
// (the vcsim/graphsim posture, §1.5 — a test double; the sovereign contract is
// the Syncer's, never this). It serves just enough of the API for the chef
// Syncer to run its real code paths — node enumeration, per-node fetch,
// removal — AND it verifies the Chef Mixlib request signature on every call, so
// the go-chef signing path is proven end-to-end with no real Chef server (the
// explicit reason harnesses are first-class for this out-of-network OSS build).
//
// Signature verification re-signs the canonical request with the fixture key
// and compares bytes. Chef sign v1.0 (RSA private-encrypt) and v1.3
// (PKCS1v15) are both deterministic, so an identical result proves the client
// signed the correct canonical request with the correct key; a wrong key or a
// tampered signed header yields different bytes → 401. This reuses go-chef's own
// exported signing primitives (no reimplemented crypto).
package chefsim

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"sync"

	chefapi "github.com/go-chef/chef"
)

// errBadSignature is returned by verify when the reassembled X-Ops-Authorization
// headers do not match a fresh signature over the request's canonical content.
var errBadSignature = errors.New("chefsim: bad or missing Chef request signature")

// Node is the node wire shape this sim serves — the subset of chef.Node the
// Syncer consumes. Automatic holds ohai attributes.
type Node struct {
	Name        string         `json:"name"`
	Environment string         `json:"chef_environment,omitempty"`
	RunList     []string       `json:"run_list,omitempty"`
	Automatic   map[string]any `json:"automatic,omitempty"`
}

// Sim is the in-memory fixture service for one org + signing client.
type Sim struct {
	mu         sync.Mutex
	org        string
	clientName string
	key        *rsa.PrivateKey
	nodes      map[string]Node
}

// GenerateKey returns a fresh RSA key and its PKCS#1 PEM encoding — the fixture
// keypair a test hands to both the Sim (to verify) and the Config (to sign).
func GenerateKey() (*rsa.PrivateKey, string, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, "", err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return key, string(pemBytes), nil
}

// New returns a Sim verifying requests signed by clientName with key, scoped to
// org. Seed nodes with Set.
func New(org, clientName string, key *rsa.PrivateKey) *Sim {
	return &Sim{org: org, clientName: clientName, key: key, nodes: map[string]Node{}}
}

// Set adds or replaces a node (also usable via the POST /_sim/nodes hook).
func (s *Sim) Set(n Node) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes[n.Name] = n
}

// Handler serves the node API (signature-gated) plus unauthenticated test hooks.
func (s *Sim) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /organizations/{org}/nodes", s.authed(s.list))
	mux.HandleFunc("GET /organizations/{org}/nodes/{name}", s.authed(s.get))
	mux.HandleFunc("POST /_sim/nodes", s.simSet)
	mux.HandleFunc("POST /_sim/remove", s.simRemove)
	return mux
}

// authed wraps a handler with Chef signature verification.
func (s *Sim) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.verify(r); err != nil {
			http.Error(w, `{"error":["`+err.Error()+`"]}`, http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func (s *Sim) list(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	base := "https://" + r.Host + "/organizations/" + s.org + "/nodes/"
	for name := range s.nodes {
		out[name] = base + name
	}
	writeJSON(w, out)
}

func (s *Sim) get(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[r.PathValue("name")]
	if !ok {
		http.Error(w, `{"error":["node not found"]}`, http.StatusNotFound)
		return
	}
	writeJSON(w, n)
}

func (s *Sim) simSet(w http.ResponseWriter, r *http.Request) {
	var n Node
	if err := json.NewDecoder(r.Body).Decode(&n); err != nil || n.Name == "" {
		http.Error(w, "node.name required", http.StatusBadRequest)
		return
	}
	s.Set(n)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Sim) simRemove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	delete(s.nodes, body.Name)
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// verify reconstructs the canonical signing content from the request's own
// X-Ops-* headers, re-signs it with the fixture key, and compares to the
// reassembled X-Ops-Authorization-N chunks.
func (s *Sim) verify(r *http.Request) error {
	if r.Header.Get("X-Ops-UserId") != s.clientName {
		return errBadSignature
	}
	var chunks []string
	for i := 1; ; i++ {
		v := r.Header.Get(fmt.Sprintf("X-Ops-Authorization-%d", i))
		if v == "" {
			break
		}
		chunks = append(chunks, v)
	}
	if len(chunks) == 0 {
		return errBadSignature
	}
	got := strings.Join(chunks, "")

	ver := chefapi.AuthVersion10
	if strings.Contains(r.Header.Get("X-Ops-Sign"), "1.3") {
		ver = chefapi.AuthVersion13
	}
	vals := map[string]string{
		"Method":                   r.Method,
		"X-Ops-Content-Hash":       r.Header.Get("X-Ops-Content-Hash"),
		"X-Ops-Timestamp":          r.Header.Get("X-Ops-Timestamp"),
		"X-Ops-UserId":             r.Header.Get("X-Ops-UserId"),
		"X-Ops-Server-API-Version": r.Header.Get("X-Ops-Server-API-Version"),
	}
	if ver == chefapi.AuthVersion13 {
		vals["Path"] = path.Clean(r.URL.Path)
		vals["X-Ops-Sign"] = "version=1.3"
	} else {
		vals["Hashed Path"] = chefapi.HashStr(path.Clean(r.URL.Path))
	}
	content := chefapi.AuthConfig{AuthenticationVersion: ver}.SignatureContent(vals)

	var expected []byte
	var err error
	if ver == chefapi.AuthVersion13 {
		expected, err = chefapi.GenerateDigestSignature(s.key, content)
	} else {
		expected, err = chefapi.GenerateSignature(s.key, content)
	}
	if err != nil {
		return err
	}
	if strings.Join(chefapi.Base64BlockEncode(expected, 60), "") != got {
		return errBadSignature
	}
	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
