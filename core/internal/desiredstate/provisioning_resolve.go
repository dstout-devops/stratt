package desiredstate

import (
	"context"
	"slices"
	"strings"

	"github.com/dstout-devops/stratt/core/internal/capability"
	"github.com/dstout-devops/stratt/core/internal/graph"
	"github.com/dstout-devops/stratt/types"
)

// resolveProvisioning binds an Intent kind's `requires: [provisioning]` to a concrete provider +
// gated build Workflow (ADR-0110 D3/D4), over the store's VERIFIED-provider index (ADR-0104 slice 2)
// and the in-scope capability-bindings. intentKind is the bare kind (no "Intent/" prefix). It is the
// store-backed assembler around the pure capability.Resolve — fail-closed is the resolver's job.
func (c *Controller) resolveProvisioning(ctx context.Context, intentKind string) (capability.Result, error) {
	providers, err := verifiedProvisioningProviders(ctx, c.Store)
	if err != nil {
		return capability.Result{}, err
	}
	bindings, err := c.Store.ListCapabilityBindings(ctx)
	if err != nil {
		return capability.Result{}, err
	}
	return capability.Resolve(types.CapProvisioning, intentKind, providers, bindings), nil
}

// verifiedProvisioningProviders assembles the VERIFIED providers that `provides` provisioning and
// advertise per-kind build Workflows (their `provisions` map) — a phantom/unverified provider is
// excluded (fail-closed, ADR-0104 D1), and a verified provider without a `provisions` map cannot
// build anything so it is skipped too.
func verifiedProvisioningProviders(ctx context.Context, store *graph.Store) ([]capability.Provider, error) {
	verifs, err := store.ListProviderVerifications(ctx)
	if err != nil {
		return nil, err
	}
	verified := map[string]bool{}
	for _, v := range verifs {
		if v.Verified {
			verified[v.Kind+"/"+v.Name] = true
		}
	}
	var out []capability.Provider
	acts, err := store.ListActuators(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range acts {
		if verified["actuator/"+a.Name] && slices.Contains(a.Provides, types.CapProvisioning) && len(a.Provisions) > 0 {
			out = append(out, capability.Provider{Name: a.Name, Provisions: a.Provisions})
		}
	}
	conns, err := store.ListConnectors(ctx)
	if err != nil {
		return nil, err
	}
	for _, cn := range conns {
		if verified["connector/"+cn.Name] && slices.Contains(cn.Provides, types.CapProvisioning) && len(cn.Provisions) > 0 {
			out = append(out, capability.Provider{Name: cn.Name, Provisions: cn.Provisions})
		}
	}
	return out, nil
}

// shortIntentKind strips the "Intent/" prefix for capability resolution + provisions/binding keys.
func shortIntentKind(kind string) string { return strings.TrimPrefix(kind, "Intent/") }

// provisionFindingDetail enriches a build Finding's detail with the resolution outcome (ADR-0110
// D4): a RESOLVED build names the bound provider + the gated build Workflow to launch; a PENDING or
// AMBIGUOUS one carries the observable reason and NO workflow — fail-closed, nothing to launch until
// the operator resolves it (§1.8 / §2.4).
func provisionFindingDetail(r capability.Result, base map[string]any) map[string]any {
	base["requires"] = []string{types.CapProvisioning}
	if r.Status == capability.StatusResolved {
		base["provider"] = r.Provider
		base["buildWorkflow"] = r.Workflow
		base["reason"] = "declared but not built — launch the gated build Workflow (never auto-run, §5 Flow 1)"
		return base
	}
	base["unresolved"] = r.Reason
	base["reason"] = "declared but not built, and provisioning is UNRESOLVED — " + r.Reason
	return base
}
