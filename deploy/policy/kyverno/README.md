# Stratt in-cluster pod-security enforcement (SEC-4, charter §7.1)

Stratt **ships** hardened pods — the EE Job runs sandboxed (non-root, drop-ALL,
no-privilege-escalation, seccomp, resource-capped; ADR-0051 / enterprise-readiness
SEC-1), and the control-plane, plugin, backup, forwarder, and migration pods carry
the same security contexts. But shipping a hardened default is not the same as
*enforcing* it: a hand-edited manifest, a Helm override, or a future plugin could
still land a privileged pod in the estate. §7.1 calls for a shipped policy set that
**enforces** the floor in-cluster. Policy is external, over a seam — the same
posture as the OPA governance PDP (`../`), never trusted to our own templates.

Two mechanisms, pick per cluster (they can coexist):

## 1. Native Pod Security Admission — zero dependency (recommended baseline)

Built into Kubernetes (≥1.25). Label the Stratt namespaces `restricted` and the
API server rejects any violating pod at admission — no controller to run.

```sh
kubectl apply -f ../psa/namespace-restricted.yaml       # declarative, or:
kubectl label ns stratt stratt-jobs \
  pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/enforce-version=latest
```

Start with `warn`/`audit` (the example sets both) to surface violations, then flip
`enforce` on once every workload complies. This covers the PSS `restricted` profile.

## 2. Kyverno ClusterPolicies — richer, report + mutate capable

If you already run Kyverno, apply the policy set for the same `restricted` floor
**plus** extras PSA can't express (resource-bound auditing), with policy reports:

```sh
kubectl apply -f pod-security-restricted.yaml   # Enforce: PSS restricted
kubectl apply -f require-resource-bounds.yaml   # Audit: memory+cpu limits present
```

Both target namespaces labelled `stratt.dev/pod-security: restricted` (the PSA
example sets it too, so the two mechanisms line up). `pod-security-restricted.yaml`
uses Kyverno's built-in `validate.podSecurity` — the whole PSS restricted profile
in one rule, not hand-rolled checks.

## What "restricted" requires (and we already satisfy)

`runAsNonRoot`, `allowPrivilegeEscalation: false`, all capabilities dropped, a
`RuntimeDefault`/`Localhost` seccomp profile, no privileged containers, no host
namespaces/ports, no hostPath volumes. Every pod the chart renders passes it — so
enforcing this floor hardens the estate without blocking Stratt's own workloads.
`readOnlyRootFilesystem` is set on our long-lived pods but is **not** part of PSS
restricted and is **not** enforced here (the EE Job does not yet set it — SEC-1
follow-up), so admission enforcement never blocks a legitimate Job.

## Verify before enforcing

```sh
# Dry-run the profile against what the chart would deploy:
helm template stratt deploy/charts/stratt | \
  kubectl label --local -f - --dry-run=client ... # or kyverno CLI:
kyverno apply pod-security-restricted.yaml --resource <(helm template stratt deploy/charts/stratt)
```
