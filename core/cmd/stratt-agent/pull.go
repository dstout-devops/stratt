package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dstout-devops/stratt/core/internal/bundle"
	"github.com/dstout-devops/stratt/types"
)

// servePull is the pull-mode loop (ADR-0032): an egress-only Site pulls its
// assigned signed Bundle from an OCI registry on a cadence, VERIFIES the cosign
// signature + pinned digest BEFORE unpacking, and runs it through the SAME
// dispatch.Dispatcher.Run. A tampered/unsigned Bundle is refused loudly (§1.8)
// and never executed.
//
// v1 config is env-direct (STRATT_BUNDLE_REF/DIGEST/PUBKEY); a signed OCI
// assignment-index the agent resolves for itself is the documented follow-up.
func (ag *agent) servePull(ctx context.Context) error {
	ref := os.Getenv("STRATT_BUNDLE_REF")
	if ref == "" {
		return fmt.Errorf("pull mode requires STRATT_BUNDLE_REF")
	}
	pinnedDigest := os.Getenv("STRATT_BUNDLE_DIGEST") // the Assignment integrity anchor
	pubPath := os.Getenv("STRATT_BUNDLE_PUBKEY")
	if pubPath == "" {
		return fmt.Errorf("pull mode requires STRATT_BUNDLE_PUBKEY (pinned cosign public key)")
	}
	pubPEM, err := os.ReadFile(pubPath)
	if err != nil {
		return fmt.Errorf("read pinned public key: %w", err)
	}
	interval := 30 * time.Second
	if v := os.Getenv("STRATT_PULL_INTERVAL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			interval = time.Duration(n) * time.Second
		}
	}

	ag.log.Info("pull loop started", "ref", ref, "pinnedDigest", pinnedDigest, "interval", interval.String())
	ag.pullOnce(ctx, ref, pinnedDigest, pubPEM)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			ag.pullOnce(ctx, ref, pinnedDigest, pubPEM)
		}
	}
}

// pullOnce fetches, verifies, and (if new) runs the Bundle. Verification failure
// is a hard refusal that emits a run.failed event naming the Site + reason.
func (ag *agent) pullOnce(ctx context.Context, ref, pinnedDigest string, pubPEM []byte) {
	p, err := bundle.Pull(ctx, ref)
	if err != nil {
		ag.log.Warn("bundle pull failed", "ref", ref, "err", err)
		return
	}
	spec, actuator, err := bundle.VerifiedSpec(ctx, p, pubPEM, pinnedDigest)
	if err != nil {
		// §1.8/§1.5: a bad Bundle fails LOUDLY and is never unpacked/run.
		ag.log.Error("bundle REFUSED", "ref", ref, "digest", p.ManifestDigest, "reason", err)
		ag.emitRefusal(ctx, ref, p.ManifestDigest, err)
		return
	}
	if ag.lastRun == p.ManifestDigest {
		return // already applied this Bundle
	}
	interp, ok := ag.interp[actuator]
	if !ok {
		ag.log.Error("no interpreter for verified bundle", "actuator", actuator)
		ag.emitRefusal(ctx, ref, p.ManifestDigest, fmt.Errorf("no interpreter for %q", actuator))
		return
	}
	runID := "pull-" + ag.site + "-" + shortDigest(p.ManifestDigest)
	ag.log.Info("bundle verified — running", "ref", ref, "digest", p.ManifestDigest, "run", runID)
	res, err := ag.dispatcher.Run(ctx, runID, 0, spec, interp, nil, nil)
	if err != nil {
		ag.log.Error("bundle run failed", "run", runID, "err", err)
		return
	}
	ag.lastRun = p.ManifestDigest
	ag.log.Info("bundle run complete", "run", runID, "succeeded", res.Succeeded)
}

// emitRefusal publishes a run.failed event so a refused Bundle is visible on the
// hub's run-event stream via the leaf (§1.8: descent shows where and why).
func (ag *agent) emitRefusal(ctx context.Context, ref, digest string, cause error) {
	if ag.bus == nil {
		return
	}
	runID := "pull-" + ag.site + "-" + shortDigest(digest)
	_ = ag.bus.Publish(ctx, types.RunEvent{
		RunID: runID, Seq: 1, Kind: "run.failed", Site: ag.site,
		Payload: map[string]any{
			"reason":    "bundle-verification-refused",
			"bundleRef": ref,
			"digest":    digest,
			"detail":    cause.Error(),
		},
	})
}

func shortDigest(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		return d[:12]
	}
	return d
}
