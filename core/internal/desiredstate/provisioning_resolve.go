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
//
// Provider selection is ENVIRONMENT-SCOPED (ADR-0113 D2, extending ADR-0057): both the verified
// providers and the bindings are filtered to the daemon's ActiveEnvironment, so an environment is
// the substrate/sovereignty boundary (vSphere in one, EC2/opentofu in another). This is additive
// scope — membership only, never precedence (§2.4); ambiguity WITHIN an environment still fails
// closed in capability.Resolve.
func (c *Controller) resolveProvisioning(ctx context.Context, intentKind string) (capability.Result, error) {
	env := c.Store.ActiveEnvironment()
	providers, err := verifiedProvisioningProviders(ctx, c.Store, env, types.CapProvisioning)
	if err != nil {
		return capability.Result{}, err
	}
	allBindings, err := c.Store.ListCapabilityBindings(ctx)
	if err != nil {
		return capability.Result{}, err
	}
	return capability.Resolve(types.CapProvisioning, intentKind, providers, inScopeBindings(allBindings, env)), nil
}

// verifiedProvisioningProviders assembles the VERIFIED, in-environment providers that `provides`
// provisioning and advertise per-kind build Workflows. Store I/O only; the selection is the pure
// assembleProvisioningProviders (testable without a DB).
func verifiedProvisioningProviders(ctx context.Context, store *graph.Store, env, capClass string) ([]capability.Provider, error) {
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
	acts, err := store.ListActuators(ctx)
	if err != nil {
		return nil, err
	}
	conns, err := store.ListConnectors(ctx)
	if err != nil {
		return nil, err
	}
	return assembleProvisioningProviders(verified, acts, conns, env, capClass), nil
}

// resolveDecommission binds an Intent kind to a concrete teardown Workflow (ADR-0114 D4), the symmetric
// counterpart to resolveProvisioning: it resolves the SAME verified, in-environment provisioning
// providers, but over their `decommissions` map instead of `provisions`. Because the teardown targets an
// Entity identified by a provider-specific scheme (e.g. vcenter.uuid → vcenter), env-scoped class
// resolution lands on the build provider in the common single-provider case; fail-closed on ambiguity.
func (c *Controller) resolveDecommission(ctx context.Context, intentKind string) (capability.Result, error) {
	env := c.Store.ActiveEnvironment()
	providers, err := decommissionProviders(ctx, c.Store, env)
	if err != nil {
		return capability.Result{}, err
	}
	allBindings, err := c.Store.ListCapabilityBindings(ctx)
	if err != nil {
		return capability.Result{}, err
	}
	return capability.Resolve(types.CapProvisioning, intentKind, providers, inScopeBindings(allBindings, env)), nil
}

// decommissionProviders assembles the verified, in-environment providers that `provides` provisioning
// and advertise per-kind TEARDOWN Workflows — the pure resolver keys on the `decommissions` map (passed
// as capability.Provider.Provisions, which is a generic kind→workflow map).
func decommissionProviders(ctx context.Context, store *graph.Store, env string) ([]capability.Provider, error) {
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
	acts, err := store.ListActuators(ctx)
	if err != nil {
		return nil, err
	}
	conns, err := store.ListConnectors(ctx)
	if err != nil {
		return nil, err
	}
	return assembleTeardownProviders(verified, acts, conns, env), nil
}

// assembleTeardownProviders is the PURE selection for teardown — the `decommissions` sibling of
// assembleProvisioningProviders, same rules over the other map.
//
// Extracted from decommissionProviders rather than written beside it: the load-time check needs the
// same selection with every declared provider treated as verified (reachableBuilders), and a second
// copy of a selection rule is how the provisioning and teardown halves drift apart. This repo has
// found that failure three times already.
func assembleTeardownProviders(verified map[string]bool, acts []types.Actuator, conns []types.Connector, env string) []capability.Provider {
	var out []capability.Provider
	for _, a := range acts {
		if verified["actuator/"+a.Name] && types.InScope(a.ScopedEnvironments(), env) &&
			slices.Contains(a.Provides, types.CapProvisioning) && len(a.Decommissions) > 0 {
			out = append(out, capability.Provider{Name: a.Name, Workflows: a.Decommissions, Substrate: a.Substrate})
		}
	}
	for _, cn := range conns {
		if verified["connector/"+cn.Name] && types.InScope(cn.ScopedEnvironments(), env) &&
			slices.Contains(cn.Provides, types.CapProvisioning) && len(cn.Decommissions) > 0 {
			out = append(out, capability.Provider{Name: cn.Name, Workflows: cn.Decommissions, Substrate: cn.Substrate})
		}
	}
	return out
}

// assembleProvisioningProviders is the PURE selection (ADR-0104 D1 / ADR-0113 D2): a provider is
// included only if it is verified, `provides` provisioning, advertises ≥1 build Workflow, AND is in
// scope for env (types.InScope membership). A phantom/unverified provider, a provider without a
// `provisions` map, or one scoped to a different environment is excluded — all fail-closed.
// capClass is a PARAMETER rather than the hardcoded provisioning constant it used to be. A nested
// capability Step (ADR-0139 D3) may name a class that is not `provisioning`, and an assembler that
// silently ignored it would return provisioning providers for every question asked — resolving to a
// real, wrong Workflow rather than failing closed, which is the worst available outcome.
func assembleProvisioningProviders(verified map[string]bool, acts []types.Actuator, conns []types.Connector, env, capClass string) []capability.Provider {
	var out []capability.Provider
	for _, a := range acts {
		if verified["actuator/"+a.Name] && types.InScope(a.ScopedEnvironments(), env) &&
			slices.Contains(a.Provides, capClass) && len(a.Provisions) > 0 {
			out = append(out, capability.Provider{Name: a.Name, Workflows: a.Provisions, Substrate: a.Substrate})
		}
	}
	for _, cn := range conns {
		if verified["connector/"+cn.Name] && types.InScope(cn.ScopedEnvironments(), env) &&
			slices.Contains(cn.Provides, capClass) && len(cn.Provisions) > 0 {
			out = append(out, capability.Provider{Name: cn.Name, Workflows: cn.Provisions, Substrate: cn.Substrate})
		}
	}
	return out
}

// inScopeBindings filters capability-bindings to those in scope for env (ADR-0113 D2, membership per
// ADR-0057) — pure + testable. An out-of-environment binding must not select a provider in another
// environment (that would be a cross-environment precedence leak, §2.4).
func inScopeBindings(all []types.CapabilityBinding, env string) []types.CapabilityBinding {
	out := make([]types.CapabilityBinding, 0, len(all))
	for _, b := range all {
		if types.InScope(b.ScopedEnvironments(), env) {
			out = append(out, b)
		}
	}
	return out
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

// ── Remediation resolution (ADR-0135 D2/D3) ─────────────────────────────────────────────

// resolveRemediation binds a Blueprint route's `remediationCapability` to a concrete provider +
// convergence Workflow, over the SAME verified-provider index and in-scope bindings provisioning
// uses. intentKind is the bare kind (no "Intent/" prefix).
//
// It exists beside resolveProvisioning rather than inside the compiler because the verified index
// and the environment filter are estate concerns, and duplicating them would give remediation a
// second, drifting notion of which providers count.
func (c *Controller) resolveRemediation(ctx context.Context, capClass, intentKind string) (capability.Result, error) {
	env := c.Store.ActiveEnvironment()
	providers, err := verifiedRemediationProviders(ctx, c.Store, env, capClass)
	if err != nil {
		return capability.Result{}, err
	}
	allBindings, err := c.Store.ListCapabilityBindings(ctx)
	if err != nil {
		return capability.Result{}, err
	}
	return capability.Resolve(capClass, intentKind, providers, inScopeBindings(allBindings, env)), nil
}

// verifiedRemediationProviders is the store-backed half; assembleRemediationProviders is the pure
// selection, testable without a DB.
func verifiedRemediationProviders(ctx context.Context, store *graph.Store, env, capClass string) ([]capability.Provider, error) {
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
	acts, err := store.ListActuators(ctx)
	if err != nil {
		return nil, err
	}
	conns, err := store.ListConnectors(ctx)
	if err != nil {
		return nil, err
	}
	return assembleRemediationProviders(verified, acts, conns, env, capClass), nil
}

// assembleRemediationProviders mirrors assembleProvisioningProviders exactly, one map along: a
// provider counts only if it is VERIFIED, `provides` the class the route asked for, advertises a
// remediation Workflow, and is in scope for env. Every exclusion is fail-closed — an unverified or
// out-of-environment provider must never satisfy a route (§2.4).
func assembleRemediationProviders(verified map[string]bool, acts []types.Actuator, conns []types.Connector, env, capClass string) []capability.Provider {
	var out []capability.Provider
	for _, a := range acts {
		if verified["actuator/"+a.Name] && types.InScope(a.ScopedEnvironments(), env) &&
			slices.Contains(a.Provides, capClass) && len(a.Remediates) > 0 {
			out = append(out, capability.Provider{Name: a.Name, Workflows: a.Remediates, Substrate: a.Substrate})
		}
	}
	for _, cn := range conns {
		if verified["connector/"+cn.Name] && types.InScope(cn.ScopedEnvironments(), env) &&
			slices.Contains(cn.Provides, capClass) && len(cn.Remediates) > 0 {
			out = append(out, capability.Provider{Name: cn.Name, Workflows: cn.Remediates})
		}
	}
	return out
}

// ResolveBuildWorkflow is the launch-time resolution a nested capability Step needs (ADR-0139 D3):
// (capability class, Intent kind) → the bound provider's build Workflow.
//
// It is the SAME store-backed assembly + pure capability.Resolve the compiler uses, exported rather
// than duplicated. A second resolver would be a second answer to "which provider builds this", and
// two resolvers that can disagree is the ambiguity §2.4 exists to refuse — the compiler and a
// nested Step must reach the same provider or the estate means two different things depending on
// who is asking.
//
// Environment-scoped and fail-closed exactly as the compiler's path is: PENDING and AMBIGUOUS carry
// the resolver's own reason, because "no verified provider" and "two providers, add a binding" send
// the reader to different places (§1.8).
func ResolveBuildWorkflow(ctx context.Context, store *graph.Store, capClass, intentKind string) (capability.Result, error) {
	env := store.ActiveEnvironment()
	providers, err := verifiedProvisioningProviders(ctx, store, env, capClass)
	if err != nil {
		return capability.Result{}, err
	}
	allBindings, err := store.ListCapabilityBindings(ctx)
	if err != nil {
		return capability.Result{}, err
	}
	return capability.Resolve(capClass, intentKind, providers, inScopeBindings(allBindings, env)), nil
}
