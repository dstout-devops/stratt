// Command graphsim runs the dev-harness Microsoft Graph stand-in (ADR-0014):
// the vcsim posture for the msgraph Connector. Dev only — never deployed.
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/dstout-devops/stratt/core/internal/connectors/msgraph/graphsim"
)

func main() {
	addr := os.Getenv("GRAPHSIM_ADDR")
	if addr == "" {
		addr = ":8090"
	}
	base := os.Getenv("GRAPHSIM_BASE")
	if base == "" {
		base = "http://localhost:8090"
	}
	sim := graphsim.New(base)
	log.Printf("graphsim listening on %s (links via %s)", addr, base)
	srv := &http.Server{Addr: addr, Handler: sim.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
