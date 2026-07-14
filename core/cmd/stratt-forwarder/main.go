// Command stratt-forwarder ships the one audit stream to a SIEM (ADR-0034). It
// runs as a long-lived pod, reads audit batches from the platform API by a
// server-owned cursor, and delivers them through a vendor-neutral driver
// (Splunk HEC / syslog / OTel-logs) — committing the cursor only on success, so
// no audit record is ever dropped (§1.8). The SIEM credential is injected into
// this pod at spawn (§2.5); the control plane holds only a pointer.
//
// Config (env):
//
//	STRATT_SERVER              platform API base (default http://localhost:8080)
//	STRATT_FORWARDER_SINK      the SIEM Sink name (required)
//	STRATT_FORWARDER_TOKEN     platform API bearer token (OIDC) — the forwarder's
//	                           identity; it must hold reader on audit:log
//	STRATT_FORWARDER_PRINCIPAL dev-header identity (when the token path is off)
//	STRATT_FORWARDER_CRED_DIR  injected SIEM credential mount (default
//	                           /runner/credentials/siem)
//	STRATT_FORWARDER_BATCH     max events per batch (default 200)
//	STRATT_FORWARDER_INTERVAL  poll/backoff base seconds (default 5)
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/dstout-devops/stratt/core/internal/forwarder"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	sink := os.Getenv("STRATT_FORWARDER_SINK")
	if sink == "" {
		log.Error("STRATT_FORWARDER_SINK is required")
		os.Exit(2)
	}
	cfg := forwarder.Config{
		Server:        env("STRATT_SERVER", "http://localhost:8080"),
		Sink:          sink,
		CredentialDir: env("STRATT_FORWARDER_CRED_DIR", "/runner/credentials/siem"),
		BatchLimit:    atoi(os.Getenv("STRATT_FORWARDER_BATCH"), 200),
		Interval:      time.Duration(atoi(os.Getenv("STRATT_FORWARDER_INTERVAL"), 5)) * time.Second,
		Log:           log,
	}
	// The forwarder authenticates to the platform as its own Principal (§1.6):
	// a bearer token in production, the dev header otherwise.
	if tok := os.Getenv("STRATT_FORWARDER_TOKEN"); tok != "" {
		cfg.AuthHeader, cfg.AuthValue = "Authorization", "Bearer "+tok
	} else if p := os.Getenv("STRATT_FORWARDER_PRINCIPAL"); p != "" {
		cfg.AuthHeader, cfg.AuthValue, cfg.PrincipalKind = "X-Stratt-Principal", p, "agent"
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := cfg.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("forwarder stopped", "err", err)
		os.Exit(1)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func atoi(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
