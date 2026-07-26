#!/usr/bin/env bash
# run.sh — the turnkey runner for the "vSphere: provision a VM + watch the graph come alive" demo
# (ADR-0116 D2). Drives BOTH halves of the estate model on one substrate:
#   READ  — the boot-wired vcenter Syncer projects the vcsim topology (regions/AZs/datastores/VMs) into
#           a live graph; the demo's Views make it queryable.
#   WRITE — a gated Workflow provisions a new VM through the vcenter/create-vm Action, which the Syncer's
#           next OBSERVE then picks up — closing the build→observe loop.
#
# `task demo:vsphere-only:run` stands the floor up (kind + strattd + the vcenter plugin + vcsim + the
# staged CaC estate + a seeded vcsim topology) before invoking this. Needs curl + jq + kubectl.
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
echo "│ vcsim is a real vCenter API (govmomi): provisioning executes; no guest OS boots."
echo "└───────────────────────────────────────────────────────────────────────────"

echo "demo: port-forward svc/stratt ${LPORT}->8080 (ns ${NS})"
kc -n "$NS" port-forward svc/stratt "${LPORT}:8080" >/dev/null 2>&1 &
PF_PID=$!
trap 'kill "$PF_PID" 2>/dev/null || true' EXIT
for _ in $(seq 1 30); do curl -fsS "${ROOT}/healthz" >/dev/null 2>&1 && break; sleep 1; done

# ── READ: wait for the vcenter Syncer to project the vcsim topology into the graph ─────────────────
echo "demo: awaiting the vcenter Syncer's first OBSERVE (the graph coming alive)…"
vms=0
for _ in $(seq 1 40); do
    vms=$(count dev-vms)
    [ "$vms" -gt 0 ] 2>/dev/null && break
    sleep 3
done
[ "$vms" -gt 0 ] 2>/dev/null || { echo "FAIL: the dev-vms View never populated (Syncer/vcsim path broken)"; exit 1; }
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
echo "demo: awaiting the Syncer to observe the built web-01 (build → observe closure)…"
seen=""
for _ in $(seq 1 20); do
    seen=$(api GET "/views/dev-vms/entities" 2>/dev/null | jq -r '.entities[]? | select((.identityKeys.name // "") == "web-01" or (.labels["stratt.intent/instance"] // "") == "web-01") | "web-01"' | head -1)
    [ "$seen" = "web-01" ] && break
    sleep 3
done
if [ "$seen" = "web-01" ]; then
    echo "  web-01 now appears in the dev-vms View — the write is visible in the read-model"
else
    echo "  (web-01 not yet observed in dev-vms — the Syncer picks it up on its next cycle)"
fi

echo
echo "demo: DONE — Stratt projected a live vSphere graph AND provisioned a VM through a gated Workflow (fidelity: ${fidelity})."
printf "  Final graph: regions/AZs: %s   datastores: %s   VMs: %s\n" "$(count availability-zones)" "$(count datastores)" "$(count dev-vms)"
echo "  Explore the graph in the UI:  (cd ui && npm run dev)  → Views → dev-vms / availability-zones / datastores"
echo "  Watch the descent:            UI → Runs → WorkflowRun ${run_id}"
echo "  Clean up:       task demo:vsphere-only:down   (or full teardown: task dev:kind:down && task dev:down)"
