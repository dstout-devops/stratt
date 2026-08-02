#!/usr/bin/env bash
# run.sh — the turnkey runner for the "app install with a certificate" demo (ADR-0116 D2,
# ADR-0117 f/h). Drives the descent on a Stratt floor whose desired state IS this demo's estate
# (staged into the declarations mount + enforced by the reconcile controller — actuators,
# connectors and credential-refs are CaC-only, §2.2/2.3, never `stratt apply`).
#
# It launches the gated app-install-with-cert Workflow, approves the platform-admins gate as the
# dev-header bootstrap-admin Principal, waits for convergence, and then asserts FOUR things — each
# of which would have failed silently before the decision that made it true:
#
#   1. The app really serves TLS on the node, on a certificate minted by community.crypto from
#      the EE this Step's Actuator declares (D3/D3a) — read back off the wire, not off a file.
#   2. The Run reported its observed app.config back into the graph, under a bounded grant (MF3).
#   3. A play that matches no host FAILS instead of reporting green (ADR-0117 D5, follow-up h).
#   4. A Step declaring no reach method for a host nothing observed is REFUSED before ansible is
#      spawned, and the refusal names the target and both remedies (ADR-0158 D1/D3).
#
# `task demo:app-cert:run` stands the floor up (kind + strattd + the declared plugin + the crypto
# EE + the managed node + this estate) before invoking this script. Needs curl + jq + kubectl.
#
# Env: KUBECTL, KUBECONTEXT, STRATT_NS, STRATT_PRINCIPAL, STRATT_LPORT.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

KUBECTL="${KUBECTL:-kubectl}"
CTX="${KUBECONTEXT:-kind-stratt-dev}"
NS="${STRATT_NS:-stratt}"
PRINCIPAL="${STRATT_PRINCIPAL:-bootstrap-admin}"
WORKFLOW="app-install-with-cert"
GUARD_WORKFLOW="vacuous-run-guard"
REACH_GUARD_WORKFLOW="unreached-target-guard"
VIEW="app-nodes"
ACTUATOR="ansible-crypto"
NODE_DEPLOY="app-node"
LPORT="${STRATT_LPORT:-18093}"
ROOT="http://127.0.0.1:${LPORT}"
API="${ROOT}/api/v1"

kc() { "$KUBECTL" --context "$CTX" "$@"; }
api() { # api METHOD PATH  — always as the bootstrap-admin Principal (dev header)
    curl -fsS -X "$1" "${API}$2" -H "X-Stratt-Principal: ${PRINCIPAL}" -H "Content-Type: application/json" "${@:3}"
}
onnode() { kc -n "$NS" exec "deploy/${NODE_DEPLOY}" -- sh -c "$1"; }

# ── Surface the declared fidelity up front (D3: never present simulated as real) ─────────────────
fidelity="$(grep -E '^fidelity:' "${HERE}/demo.yaml" | head -1 | sed 's/^fidelity:[[:space:]]*//')"
fnote="$(grep -E '^fidelityNote:' "${HERE}/demo.yaml" | head -1 | sed 's/^fidelityNote:[[:space:]]*//')"
echo "┌───────────────────────────────────────────────────────────────────────────"
echo "│ demo: app install with a certificate   fidelity: ${fidelity}"
echo "│ ${fnote}"
echo "└───────────────────────────────────────────────────────────────────────────"

echo "demo: port-forward svc/stratt ${LPORT}->8080 (ns ${NS})"
kc -n "$NS" port-forward svc/stratt "${LPORT}:8080" >/dev/null 2>&1 &
PF_PID=$!
trap 'kill "$PF_PID" 2>/dev/null || true' EXIT
for _ in $(seq 1 30); do curl -fsS "${ROOT}/healthz" >/dev/null 2>&1 && break; sleep 1; done

# ── Wait for the target to EXIST in the graph ─────────────────────────────────────────────────────
# The host is declared as data; the `declared` Syncer projects it (with mgmt.address, which the
# shim renders as ansible_host) on its own cadence. Launching before that lands would target an
# empty View — and thanks to D5 that now fails loudly rather than passing, so wait for it.
echo "demo: awaiting the managed host in View ${VIEW} (declared Syncer cadence)…"
targets=0
for _ in $(seq 1 45); do
    targets=$(api GET "/views/${VIEW}/entities" 2>/dev/null | jq -r '.entities | length' 2>/dev/null || echo 0)
    [ "${targets:-0}" -gt 0 ] && break
    sleep 2
done
[ "${targets:-0}" -gt 0 ] || { echo "FAIL: no host ever landed in View ${VIEW}"; exit 1; }
echo "  ${targets} target(s) in ${VIEW}"

# ── Wait for the Actuator to be DISPATCHABLE, not merely declared ─────────────────────────────────
# NOTE the honest gap this papers over: an EE-Job Actuator is reported enabled WITHOUT any image
# check — there is nothing to dial. So this gate proves the declaration was accepted and the grant
# parsed, not that stratt-ee-crypto:dev exists. The image is built and kind-loaded by the run task
# for exactly that reason (ADR-0117 l).
echo "demo: awaiting the ${ACTUATOR} Actuator…"
enabled=""
for _ in $(seq 1 45); do
    enabled=$(api GET "/actuators/${ACTUATOR}" 2>/dev/null | jq -r '.status.enabled // false')
    [ "$enabled" = "true" ] && break
    sleep 2
done
[ "$enabled" = "true" ] || { echo "FAIL: ${ACTUATOR} Actuator never reached status.enabled=true"; exit 1; }
ee_image="$(api GET "/actuators/${ACTUATOR}" | jq -r '.actuator.image // empty')"
[ -n "$ee_image" ] || { echo "FAIL: ${ACTUATOR} declares no EE image — the Step would silently run the default EE"; exit 1; }
echo "  ${ACTUATOR} enabled, and the API reports its declared EE: ${ee_image}"

# ── Drift → Finding → remediation, which is the product's actual Flow 2 ───────────────────────
# The demo used to POST straight to /workflows/app-install-with-cert/runs. It cannot any more,
# and that is the point: the install Workflow now DECLARES its inputs (commonName, tlsPort,
# ADR-0118 D2), and a direct launch supplies neither. The values live in the Intent, the Blueprint
# route passes them (D3), and the remediation door reads them off the compiled Baseline (4b).
#
# So the demo now drives the real path: the host has no app.config facet, that is drift ("a
# missing Facet is unmet — desired state absent is drift"), a Finding opens, and remediating it
# launches the install with the Intent's own values. Strictly more faithful AND it removes the
# last hardcoded copy of 443.
echo "demo: awaiting the drift Finding (host has no app.config yet → desired state absent)…"
finding_id=""
for _ in $(seq 1 60); do
    finding_id=$(api GET "/findings?status=open" 2>/dev/null |
        jq -r '.[]? | select(.baseline | test("tls-app")) | .id' | head -1)
    [ -n "$finding_id" ] && [ "$finding_id" != "null" ] && break
    sleep 3
done
[ -n "$finding_id" ] && [ "$finding_id" != "null" ] || {
    echo "FAIL: no drift Finding opened for the tls-app Baseline"
    api GET "/findings" | jq -r '.[]? | "  finding: " + .baseline + " " + .status' | head -5
    exit 1
}
echo "  Finding ${finding_id}"

# §1.8: the door RENDERS what it would launch before launching it. Assert that, because a door
# that acts without showing its hand is the thing ADR-0118 D3 refused to ship.
echo "demo: assert the remediation preview names the Workflow AND the params from the Intent"
prev="$(api GET "/findings/${finding_id}/remediation")"
jq -r '"  workflow=" + .workflow + "  params=" + (.params | tostring)' <<<"$prev"
[ "$(jq -r '.workflow' <<<"$prev")" = "${WORKFLOW}" ] || {
    echo "FAIL: the preview names workflow '$(jq -r '.workflow' <<<"$prev")', want ${WORKFLOW}"; exit 1; }
# The params must have come from the Intent, not from anything in this script or the Workflow.
[ "$(jq -r '.params.commonName' <<<"$prev")" = "app-node.stratt.svc.cluster.local" ] || {
    echo "FAIL: preview commonName = '$(jq -r '.params.commonName' <<<"$prev")' — not the Intent's"; exit 1; }
[ "$(jq -r '.params.tlsPort' <<<"$prev")" = "443" ] || {
    echo "FAIL: preview tlsPort = '$(jq -r '.params.tlsPort' <<<"$prev")' — not the Intent's"; exit 1; }
echo "  preview params came from the Intent spec, via the Blueprint route (ADR-0118 D3)"

echo "demo: remediate the Finding as ${PRINCIPAL}"
run_id=$(api POST "/findings/${finding_id}/remediation" | jq -r '.id')
[ -n "$run_id" ] && [ "$run_id" != "null" ] || { echo "FAIL: no WorkflowRun id returned"; exit 1; }
echo "  WorkflowRun ${run_id}"

echo "demo: awaiting the approval gate…"
gate_id=""
for _ in $(seq 1 30); do
    gate_id=$(api GET "/gates?status=pending" | jq -r --arg r "$run_id" '.[]? | select(.workflowRunId==$r) | .id' | head -1)
    [ -n "$gate_id" ] && [ "$gate_id" != "null" ] && break
    sleep 1
done
[ -n "$gate_id" ] && [ "$gate_id" != "null" ] || { echo "FAIL: no pending gate for run ${run_id}"; exit 1; }
echo "  approving gate ${gate_id} as ${PRINCIPAL} (platform-admins)"
api POST "/gates/${gate_id}/decision" --data '{"approve":true}' >/dev/null

echo "demo: awaiting WorkflowRun convergence (real SSH + sudo + cert issuance)…"
status=""
for _ in $(seq 1 90); do
    status=$(api GET "/workflow-runs/${run_id}" | jq -r '.workflowRun.status // .status // empty')
    case "$status" in
        succeeded) echo "  WorkflowRun succeeded"; break ;;
        failed|cancelled)
            echo "FAIL: WorkflowRun ${status}"
            api GET "/runs?workflowRunId=${run_id}" 2>/dev/null |
                jq -r '.[]? | select(.status=="failed") | "  cause: " + (.error // "(none recorded)")' | head -3
            exit 1 ;;
    esac
    sleep 2
done
[ "$status" = "succeeded" ] || { echo "FAIL: WorkflowRun did not converge (last=${status:-none})"; exit 1; }

# ── Assertion 1: the app really serves TLS, on the certificate the play minted ────────────────────
# Read off the wire, not off the disk: a file at /etc/ssl/certs proves a task ran, while a TLS
# handshake proves the app was actually installed on it.
echo "demo: assert the app serves TLS on the certificate community.crypto issued"
onnode "curl -fsS --insecure https://127.0.0.1/ | head -1"
subject="$(onnode "echo | openssl s_client -connect 127.0.0.1:443 2>/dev/null | openssl x509 -noout -subject -enddate")"
echo "${subject}" | sed 's/^/  /'
echo "${subject}" | grep -q "app-node.stratt.svc.cluster.local" || {
    echo "FAIL: the served certificate is not the one the play issued"; exit 1; }

# ── Assertion 2: the Run wrote its observed state back, under the bounded grant ───────────────────
echo "demo: assert the Run projected the observed app.config into the graph"
entity_id="$(api GET "/views/${VIEW}/entities" | jq -r '.entities[0].id // empty')"
[ -n "$entity_id" ] || { echo "FAIL: the View resolves no Entity to read facets from"; exit 1; }
facet="$(api GET "/entities/${entity_id}" | jq -c '.facets[]? | select(.namespace=="app.config")')"
port="$(jq -r '.value.port // empty' <<<"$facet")"
[ "$port" = "443" ] || { echo "FAIL: app.config.port = '${port:-<absent>}', want 443"; exit 1; }
# Provenance is the half that matters (§1.2): the value must be stamped as written by a
# RUN, not by a Syncer. Only Normalizers and Run provenance may write Entity attributes,
# and a demo that only checked the number would pass even if a Syncer had invented it.
writer="$(jq -r '.provenance.writerKind // empty' <<<"$facet")"
[ "$writer" = "run" ] || { echo "FAIL: app.config was written by '${writer:-<none>}', expected a Run"; exit 1; }
echo "  app.config.port = ${port}, writerKind=${writer} (under facetWriteScope ∩ the Actuator's grant,"
echo "    on a namespace the tls-app Blueprint owns — registration precedes writes, §2.1)"

# ── Assertion 3: the Finding RESOLVES — expectation and observation agree ─────────────────────
# The demo never asserted this before: it launched the install directly and checked the facet
# value, which passes whether or not the Baseline agrees the estate converged. Closing the loop
# is what proves the expectation and the observation are actually COMPARABLE.
#
# That is not theoretical. Attempting F9's "make the port a real number" produced a live failure
# on this very path — `facets/app.config: /port: got number, want string` — because the pinned
# Facet Contract types it as a string. Had the write-back been coerced instead of refused, the
# graph would read 443, this Finding would never close, and the estate would look converged while
# permanently drifted. Nothing coerces between types (ADR-0118 D1), and the Facet Contract is the
# end that decides which type the whole chain uses.
echo "demo: assert the drift Finding resolves now that the estate converged"
resolved=""
for _ in $(seq 1 40); do
    resolved=$(api GET "/findings/${finding_id}" | jq -r '.status')
    [ "$resolved" = "resolved" ] && break
    sleep 3
done
[ "$resolved" = "resolved" ] || {
    echo "FAIL: Finding ${finding_id} is '${resolved}', not resolved — the observed app.config does not"
    echo "      satisfy the compiled expectation (most likely a TYPE mismatch: expected number 443"
    echo "      vs observed string \"443\", which nothing coerces between)"
    api GET "/findings/${finding_id}" | jq -r '"  diff: " + (.diff | tostring)'
    exit 1
}
echo "  Finding resolved — the value the Intent declared is the value the host reports, same type"

# ── Assertion 4: a play that reaches nothing FAILS (ADR-0117 D5 / follow-up h) ────────────────────
# The behaviour that used to be a green Run: ansible-playbook exits 0 when its pattern matches no
# host. A fleet-wide change that silently reached zero machines must never look like one that
# worked. This was hand-verified the day it shipped; asserting it here is what stops it rotting.
echo "demo: assert a play that matches no host FAILS (the vacuous-run guard)"
guard_id=$(api POST "/workflows/${GUARD_WORKFLOW}/runs" | jq -r '.id')
[ -n "$guard_id" ] && [ "$guard_id" != "null" ] || { echo "FAIL: no WorkflowRun id for the guard check"; exit 1; }
guard_status=""
for _ in $(seq 1 90); do
    guard_status=$(api GET "/workflow-runs/${guard_id}" | jq -r '.workflowRun.status // .status // empty')
    case "$guard_status" in
        failed) break ;;
        succeeded) echo "FAIL: a play that actuated NOTHING reported success — the D5 guard has regressed"; exit 1 ;;
    esac
    sleep 2
done
[ "$guard_status" = "failed" ] || { echo "FAIL: the guard Run never reached a verdict (last=${guard_status:-none})"; exit 1; }
# Same client-side filter as the reach guard below, and for the same measured reason: the server
# ignores workflowRunId. This assertion passed until now only because it ran while its own Run was
# the ONLY failed one in the estate — luck of ordering, not a filter.
cause="$(api GET "/runs?workflowRunId=${guard_id}" 2>/dev/null | jq -r --arg r "$guard_id" '.[]? | select(.workflowRunId==$r) | select(.status=="failed") | .error // empty' | head -1)"
echo "  guard Run failed as designed"
echo "  cause: ${cause:-(none recorded)}"
# §1.8: failing is half the requirement — the failure must NAME the cause, or an operator is left
# staring at an empty log inferring it.
case "$cause" in
    *"no host"*|*"matched"*|*"actuated nothing"*|*"no target"*) : ;;
    *) echo "FAIL: the guard failed without naming why (§1.8): '${cause}'"; exit 1 ;;
esac

# ── Assertion 5: a target whose reach method NOBODY stated is REFUSED (ADR-0158 D1/D3) ───────────
# The behaviour that used to be a silent ssh attempt. An absent `mgmt.transport` meant "ssh" to the
# shim, but absence is overloaded across providers — awsec2 withholds the Facet deliberately (ssh is
# right) while a vSphere guest whose Tools stop LOSES its transport and keeps a cached address (ssh
# is wrong). The estate must say which, or the Run is refused.
#
# Every OTHER Step in this demo declares `connection.type: ssh`; unreached-target-guard declares no
# connection at all, which is the pre-migration shape and the one a grep for `connection:` misses.
echo "demo: assert a target with no observed transport and no declared type is REFUSED (the reach guard)"
reach_id=$(api POST "/workflows/${REACH_GUARD_WORKFLOW}/runs" | jq -r '.id')
[ -n "$reach_id" ] && [ "$reach_id" != "null" ] || { echo "FAIL: no WorkflowRun id for the reach guard"; exit 1; }
reach_status=""
for _ in $(seq 1 90); do
    reach_status=$(api GET "/workflow-runs/${reach_id}" | jq -r '.workflowRun.status // .status // empty')
    case "$reach_status" in
        failed) break ;;
        succeeded) echo "FAIL: a Run against a host whose reach method nobody stated SUCCEEDED — it connected by guessing ssh, which is the ADR-0158 D1 regression"; exit 1 ;;
    esac
    sleep 2
done
[ "$reach_status" = "failed" ] || { echo "FAIL: the reach guard never reached a verdict (last=${reach_status:-none})"; exit 1; }
# CLIENT-SIDE FILTER, and it is not belt-and-braces: `/runs?workflowRunId=` DOES NOT FILTER —
# verified live, the query returns every Run in the estate whichever id is passed. The gate lookup
# above already selects client-side for the same reason. Without this, `head -1` picks whichever
# failed Run sorts first and this assertion silently reads the vacuous guard's error instead of
# its own — which is exactly what happened on the first run of this check.
reach_cause="$(api GET "/runs?workflowRunId=${reach_id}" 2>/dev/null | jq -r --arg r "$reach_id" '.[]? | select(.workflowRunId==$r) | select(.status=="failed") | .error // empty' | head -1)"
echo "  reach guard Run failed as designed"
echo "  cause: ${reach_cause:-(none recorded)}"
# §1.8, and D3 states it explicitly: the refusal must NAME THE TARGET and BOTH remedies. Which one
# is right is not knowable from the control node — either the estate should declare ssh, or a
# provider should have observed the transport — so a message offering only one sends half the
# operators to the wrong place. Asserted separately from the failure itself.
case "$reach_cause" in
    *"app-node"*) : ;;
    *) echo "FAIL: the refusal does not name the target (§1.8): '${reach_cause}'"; exit 1 ;;
esac
case "$reach_cause" in
    *"connection.type: ssh"*) : ;;
    *) echo "FAIL: the refusal does not offer remedy 1, declaring the type: '${reach_cause}'"; exit 1 ;;
esac
case "$reach_cause" in
    *"mgmt.transport"*) : ;;
    *) echo "FAIL: the refusal does not offer remedy 2, fixing the observation: '${reach_cause}'"; exit 1 ;;
esac
echo "  …and it named the target and BOTH remedies"

echo
echo "demo: DONE — a gated Workflow installed an app with a certificate on a real host (fidelity: ${fidelity})."
echo "  Reach the app:  kubectl --context ${CTX} -n ${NS} port-forward svc/${NODE_DEPLOY} 8443:443  # → https://localhost:8443 (self-signed)"
echo "  Watch the descent in the UI:  (cd ui && npm run dev)  → Runs → WorkflowRun ${run_id}"
echo "    the install Run's stream states the EE content it ran with (kind=ee-content)"
echo "  Clean up:       task demo:app-cert:down   (or full teardown: task dev:kind:down)"
