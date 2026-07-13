// Package notify is the outbound notification subsystem (charter §8 Phase-2
// "notifications"; ADR-0027) — the outbound mirror of the inbound
// Emitter/Trigger path. It is delivery-plane infra, not a core-model Named
// Kind (§2).
//
// Shape (structural twin of triggerengine.Engine):
//
//	Notice (run.failed | finding.open | gate.pending, on STRATT_NOTICES)
//	  → Dispatcher consumes, matches every Subscription whose `on` includes
//	    the kind AND whose CEL `match` passes (additive fan-out, §2.4)
//	  → resolves the Subscription's Sink + its CredentialRef to mount POINTERS
//	  → dispatches a webhook-Actuator delivery Job (the credential is injected
//	    into the pod at spawn — the daemon never holds material, §2.5)
//	  → records the outcome on the notify_delivery status surface (§1.8).
//
// The §2.5 posture is the load-bearing decision: unlike Argo CD / Flux (which
// read the secret in-controller) or AAP (which persists it encrypted in
// Postgres), the credentialed POST executes in an execution pod so material
// never enters the control plane. The daemon composes pod specs from
// CredentialRef pointers only.
package notify
