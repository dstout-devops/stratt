package dispatch

import (
	"context"
	"strings"
	"testing"

	"github.com/dstout-devops/stratt/core/internal/actuators"
)

// ── ADR-0169 · the last door before something runs ────────────────────────────────────────────
//
// The chart gate (ADR-0168) cannot see these images: an EE-Job image is named by an Actuator in the
// ESTATE and chosen per Step. Until this, `image := spec.Image` reached the cluster unvetted.

func dispatcherRequiring(require bool) *Dispatcher {
	return &Dispatcher{cfg: Config{Namespace: "stratt", EEImage: "stratt-ee:dev", RequireImageDigests: require}}
}

func TestATaggedEEImageIsRefusedWhenTheEstateRequiresDigests(t *testing.T) {
	d := dispatcherRequiring(true)
	err := d.createJob(context.Background(), "job-1", "run-1",
		actuators.JobSpec{Image: "stratt-ee:dev"}, nil)
	if err == nil {
		t.Fatal("a floating tag must be refused before a pod exists")
	}
	for _, want := range []string{"stratt-ee:dev", "ADR-0169", "digest"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name the image and the reason; missing %q: %v", want, err)
		}
	}
}

// The FALLBACK image is not exempt. A Step naming no image still runs something, and an estate
// requiring digests means all of them — an exemption here would be the one nobody remembers.
func TestTheFallbackImageIsAlsoRefused(t *testing.T) {
	d := dispatcherRequiring(true)
	err := d.createJob(context.Background(), "job-1", "run-1", actuators.JobSpec{}, nil)
	if err == nil || !strings.Contains(err.Error(), "stratt-ee:dev") {
		t.Fatalf("the configured default image must be checked too: %v", err)
	}
}

// THE REGRESSION THAT MATTERS: every estate that exists today runs on floating tags, and this
// check is opt-in. With it off, nothing about dispatch changes.
func TestNothingChangesWhenTheEstateDoesNotRequireDigests(t *testing.T) {
	// Getting PAST the gate is the assertion, and a nil clientset makes it observable: the call
	// panics on the cluster it does not have, which can only happen after the gate let it by.
	// A refusal would return an error instead, and never reach the panic.
	defer func() {
		if recover() == nil {
			t.Fatal("the digest gate fired with RequireImageDigests=false — it must be opt-in")
		}
	}()
	d := dispatcherRequiring(false)
	_ = d.createJob(context.Background(), "job-1", "run-1", actuators.JobSpec{Image: "stratt-ee:dev"}, nil)
}

// A digest-pinned image passes the gate — otherwise the refusal is unsatisfiable.
func TestADigestPinnedImagePassesTheGate(t *testing.T) {
	// Same signal: a pinned image must reach the cluster attempt, or the refusal would be
	// unsatisfiable and no estate could ever turn the gate on.
	defer func() {
		if recover() == nil {
			t.Fatal("a digest-pinned image was refused — the gate would be unsatisfiable")
		}
	}()
	d := dispatcherRequiring(true)
	_ = d.createJob(context.Background(), "job-1", "run-1",
		actuators.JobSpec{Image: "ghcr.io/x/stratt-ee@sha256:" + strings.Repeat("a", 64)}, nil)
}
