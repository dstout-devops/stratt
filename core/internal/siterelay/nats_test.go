package siterelay_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/dstout-devops/stratt/core/internal/pluginhost"
	"github.com/dstout-devops/stratt/core/internal/siterelay"
	"github.com/dstout-devops/stratt/types"
)

// TestRelay_NATSRoundTrip is the ADR-0049 keystone over the REAL wire: the same
// host-governs-hub-side proof, but the relay Transport is NATS (per-call inbox
// subjects on the leaf). Skips when no NATS is reachable (mirrors sitegw tests).
func TestRelay_NATSRoundTrip(t *testing.T) {
	url := os.Getenv("STRATT_NATS_URL")
	if url == "" {
		url = "nats://localhost:4222"
	}
	nc, err := nats.Connect(url, nats.Timeout(2*time.Second))
	if err != nil {
		t.Skipf("no NATS at %s: %v", url, err)
	}
	defer nc.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	site := "edge-" + time.Now().Format("150405.000")
	acceptor := siterelay.NewNATSAcceptor(nc, site, "vcenter-dev")
	go func() { _ = siterelay.Serve(ctx, acceptor, siteLocalClient(t)) }()

	grant := pluginhost.Grant{
		PluginIdentity:  "vcenter-dev",
		Tier:            pluginhost.TierCommunity,
		Source:          types.Source{Kind: "vcenter", Name: "vcenter-dev"},
		IdentitySchemes: []string{"vcenter.uuid", "dns.fqdn"},
	}
	host := pluginhost.New(nil, siterelay.NewClient(siterelay.NewNATSDialer(nc, site, "vcenter-dev")), grant,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	raw, err := host.ApplyRaw(ctx, pluginhost.ApplyInvoke{Principal: "alice", Params: []byte(`{}`)})
	if err != nil {
		t.Fatalf("applyRaw over NATS relay: %v", err)
	}
	if !raw.Succeeded || len(raw.WriteBack) != 1 {
		t.Fatalf("relay round-trip: %+v", raw)
	}
	if _, leaked := raw.WriteBack[0].IdentityKeys["dns.fqdn"]; leaked {
		t.Fatalf("governance must run hub-side over the NATS relay: dns.fqdn leaked")
	}
	if raw.WriteBack[0].IdentityKeys["vcenter.uuid"] != "u42" {
		t.Fatalf("source-local identity must survive the NATS relay: %+v", raw.WriteBack[0].IdentityKeys)
	}
}
