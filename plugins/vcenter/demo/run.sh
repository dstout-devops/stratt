#!/usr/bin/env bash
# run.sh — the turnkey runner for the "vSphere: provision a VM + watch the graph come alive" demo
# (ADR-0116 D2). Drives BOTH halves of the estate model on one substrate:
#   READ  — the boot-wired vcenter Syncer projects the vspheresim topology (regions/AZs/datastores/VMs) into
#           a live graph; the demo's Views make it queryable.
#   WRITE — a gated Workflow provisions a new VM through the vcenter/create-vm Action, which the Syncer's
#           next OBSERVE then picks up — closing the build→observe loop.
#
# `task demo:vsphere-only:run` stands the floor up (kind + strattd + the vcenter plugin + vspheresim + the
# staged CaC estate + a seeded topology) before invoking this. Needs curl + jq + kubectl.
#
# Env: KUBECTL, KUBECONTEXT, STRATT_NS, STRATT_PRINCIPAL, STRATT_LPORT.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

KUBECTL="${KUBECTL:-kubectl}"
CTX="${KUBECONTEXT:-kind-stratt-dev}"
NS="${STRATT_NS:-stratt}"
PRINCIPAL="${STRATT_PRINCIPAL:-bootstrap-admin}"
WORKFLOW="vsphere-vm-build"
LPORT="${STRATT_LPORT:-18091}"
ROOT="http://127.0.0.1:${LPORT}"
API="${ROOT}/api/v1"

kc() { "$KUBECTL" --context "$CTX" "$@"; }
api() { curl -fsS -X "$1" "${API}$2" -H "X-Stratt-Principal: ${PRINCIPAL}" -H "Content-Type: application/json" "${@:3}"; }
count() { api GET "/views/$1/entities" 2>/dev/null | jq -r '.entities | length' 2>/dev/null || echo 0; }

# ── Surface the declared fidelity up front (D3) ───────────────────────────────────────────────────
fidelity="$(grep -E '^fidelity:' "${HERE}/demo.yaml" | head -1 | sed 's/^fidelity:[[:space:]]*//')"
echo "┌───────────────────────────────────────────────────────────────────────────"
echo "│ demo: vSphere — provision a VM + watch the graph come alive   fidelity: ${fidelity}"
echo "│ vspheresim is a real vCenter API (govmomi): provisioning executes AND the built VM boots a guest."
echo "└───────────────────────────────────────────────────────────────────────────"

echo "demo: port-forward svc/stratt ${LPORT}->8080 (ns ${NS})"
kc -n "$NS" port-forward svc/stratt "${LPORT}:8080" >/dev/null 2>&1 &
PF_PID=$!
trap 'kill "$PF_PID" 2>/dev/null || true' EXIT
for _ in $(seq 1 30); do curl -fsS "${ROOT}/healthz" >/dev/null 2>&1 && break; sleep 1; done

# ── READ: wait for the vcenter Syncer to project the vspheresim topology into the graph ─────────────────
echo "demo: awaiting the vcenter Syncer's first OBSERVE (the graph coming alive)…"
vms=0
for _ in $(seq 1 40); do
    vms=$(count dev-vms)
    [ "$vms" -gt 0 ] 2>/dev/null && break
    sleep 3
done
[ "$vms" -gt 0 ] 2>/dev/null || { echo "FAIL: the dev-vms View never populated (Syncer/vspheresim path broken)"; exit 1; }
echo "  the graph is live — the estate as a queryable read-model:"
printf "    regions/AZs: %s   datastores: %s   VMs: %s\n" "$(count availability-zones)" "$(count datastores)" "$vms"

# ── WRITE: launch the gated build Workflow, approve, converge ──────────────────────────────────────
echo "demo: launch Workflow ${WORKFLOW} as ${PRINCIPAL} (provision web-01)"
# The instance identity is SUPPLIED AT LAUNCH, not baked into the Workflow (ADR-0120 D2). This demo
# has no Intent/Compute, so it plays the part the provisioning reconcile plays in the reference
# estate: it names which instance to build and the labels the projection must carry, INCLUDING the
# stratt.intent/instance correlation label the next reconcile matches on.
INSTANCE="${STRATT_DEMO_INSTANCE:-web-01}"
launch_body=$(jq -nc --arg i "$INSTANCE" '{
  inputs: {
    instance: $i,
    ordinal: 1,
    projectKind: "host",
    labels: { fleet: "web", "stratt.intent/instance": $i }
  }
}')
run_id=$(api POST "/workflows/${WORKFLOW}/runs" -d "$launch_body" | jq -r '.id')
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

echo "demo: awaiting WorkflowRun convergence…"
status=""
for _ in $(seq 1 60); do
    status=$(api GET "/workflow-runs/${run_id}" | jq -r '.workflowRun.status // .status // empty')
    case "$status" in
        succeeded) echo "  WorkflowRun succeeded — web-01 built in vSphere"; break ;;
        failed|cancelled) echo "FAIL: WorkflowRun ${status}"; exit 1 ;;
    esac
    sleep 2
done
[ "$status" = "succeeded" ] || { echo "FAIL: WorkflowRun did not converge (last=${status:-none})"; exit 1; }

# ── CLOSE THE LOOP: the Syncer's next OBSERVE picks up the freshly-built VM ────────────────────────
echo "demo: awaiting the Syncer to observe the built ${INSTANCE} (build → observe closure)…"
# A HARD gate, not a note. This step used to print "not yet observed — the Syncer picks
# it up on its next cycle" and exit 0, which means the demo reported success in exactly
# the case it exists to detect: a write that never came back into the read-model.
entity_id=""
for _ in $(seq 1 20); do
    entity_id=$(api GET "/views/dev-vms/entities" 2>/dev/null |
        jq -r --arg i "$INSTANCE" '.entities[]? | select((.identityKeys.name // "") == $i or (.labels["stratt.intent/instance"] // "") == $i) | .id' | head -1)
    [ -n "$entity_id" ] && [ "$entity_id" != "null" ] && break
    sleep 3
done
[ -n "$entity_id" ] && [ "$entity_id" != "null" ] || { echo "FAIL: ${INSTANCE} was built but never observed back into dev-vms (build→observe closure broken)"; exit 1; }
echo "  ${INSTANCE} now appears in the dev-vms View — the write is visible in the read-model"

# ── AND IT BOOTED: the guest reports a coordinate the graph publishes ──────────────────────────────
# The step the stock vcsim image could not reach. A provisioned VM there was an inventory
# record with no guest, so nothing could be converged onto it and provision→configure
# could not be exercised on vSphere at all. vspheresim boots a real guest, and the Syncer
# projects what that guest reports as mgmt.address (ADR-0143) — an OBSERVED coordinate,
# never one Stratt computed.
echo "demo: awaiting the guest to report a reachability coordinate…"
addr=""
for _ in $(seq 1 20); do
    addr=$(api GET "/entities/${entity_id}" 2>/dev/null |
        jq -r '.facets[]? | select(.namespace=="mgmt.address") | .value.address // empty' | head -1)
    [ -n "$addr" ] && break
    sleep 3
done
# Fatal, because demo.yaml now CLAIMS the guest boots. A claim the runner declines to
# check is the drift the fidelity grade exists to prevent, and it would read as green.
[ -n "$addr" ] || {
    echo "FAIL: ${INSTANCE} was built and observed but never reported a coordinate."
    echo "  vspheresim is running without a guest image, or the guest exited on boot."
    echo "  check: docker compose -f deploy/dev/docker-compose.yml logs vspheresim"
    exit 1
}
echo "  ${INSTANCE} is reachable at ${addr} — the built machine can be configured, not just listed"

echo
echo "demo: DONE — Stratt projected a live vSphere graph AND provisioned a VM through a gated Workflow (fidelity: ${fidelity})."
printf "  Final graph: regions/AZs: %s   datastores: %s   VMs: %s\n" "$(count availability-zones)" "$(count datastores)" "$(count dev-vms)"
echo "  Explore the graph in the UI:  (cd ui && npm run dev)  → Views → dev-vms / availability-zones / datastores"
echo "  Watch the descent:            UI → Runs → WorkflowRun ${run_id}"
echo "  Clean up:       task demo:vsphere-only:down   (or full teardown: task dev:kind:down && task dev:down)"
