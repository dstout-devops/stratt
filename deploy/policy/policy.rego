# policy.rego — an example OPA policy that speaks Stratt's Decision contract
# (ADR-0074). This is OPERATOR governance content — it lives outside the Stratt
# spine; swap it freely. Each rule returns a Decision {outcome, reasons, obligations}.
package stratt

import rego.v1

# ── decide: the gate PEP (input = the DecisionRequest {controls, context}) ──

# A large production change needs two-person approval.
decide := d if {
	input.context.environment == "prod"
	input.context.blastRadius.entityCount > 20
	d := {
		"outcome": "require_approval",
		"reasons": [{"code": "prod-blast-radius", "message": "large production change requires two-person approval"}],
		"obligations": [{"type": "require_approval", "params": {"teams": ["platform-admins"], "count": 2}}],
	}
}

default decide := {"outcome": "allow"}

# ── admit: the admission PEP (input = the AdmissionRequest {object, controls}) ──

# Charter §3: no exportable certificate Intents.
admit := d if {
	input.object.kind == "Intent/Certificate"
	input.object.spec.exportable == true
	d := {
		"outcome": "deny",
		"reasons": [{"code": "no-exportable-certs", "message": "exportable certificate Intents are forbidden"}],
	}
}

default admit := {"outcome": "allow"}
