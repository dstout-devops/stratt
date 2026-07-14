package chef

import (
	"net/http/httptest"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/connectors/chef/chefsim"
)

// TestChefSignedRequestRoundTrip proves the go-chef Mixlib signing path works
// against a signature-verifying sim — the hazardous crypto surface exercised
// end-to-end with no real Chef server (ADR-0037). It also proves the sim's
// verifier is not a no-op: a client signing with a different key is rejected.
func TestChefSignedRequestRoundTrip(t *testing.T) {
	key, keyPEM, err := chefsim.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sim := chefsim.New("acme", "stratt", key)
	sim.Set(chefsim.Node{Name: "web-01"})
	srv := httptest.NewServer(sim.Handler())
	defer srv.Close()

	base := srv.URL + "/organizations/acme/"

	// Correctly signed: the sim verifies the signature and returns the node list.
	good := Config{ServerURL: base, ClientName: "stratt", KeyPEM: keyPEM, SourceName: "acme-chef"}
	client, err := good.chefClient()
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	nodes, err := client.Nodes.List()
	if err != nil {
		t.Fatalf("signed list rejected by verifying sim: %v", err)
	}
	if _, ok := nodes["web-01"]; !ok {
		t.Fatalf("expected web-01 in node list, got %v", nodes)
	}

	// Wrong key: the sim must reject the mismatched signature (401), proving the
	// verification is real cryptography, not a header-presence check.
	_, wrongPEM, err := chefsim.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	bad := Config{ServerURL: base, ClientName: "stratt", KeyPEM: wrongPEM, SourceName: "acme-chef"}
	badClient, err := bad.chefClient()
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	if _, err := badClient.Nodes.List(); err == nil {
		t.Fatal("sim accepted a request signed with the wrong key; verifier is a no-op")
	}
}
