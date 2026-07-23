#!/usr/bin/env bash
# genesis-selfdeploy.sh (ADR-0102) — drive the dogfood end to end: from the minimal
# genesis floor, have Stratt self-deploy a REAL service (the OpenFGA server, memory
# mode) through its OWN gated helm/deploy loop. Launches the genesis-authz-deploy
# Workflow, approves the platform-admins Gate as the dev-header bootstrap-admin
# Principal, waits for the WorkflowRun to converge, and confirms the workload exists.
#
# Invoked by `task dev:genesis:selfdeploy` (which first ensures the stratt-authz
# namespace + gate-marker Secret). Assumes `task dev:genesis` already ran.
#
# Env: KUBECTL (path), KUBECONTEXT (kube context). Needs curl + jq.
set -euo pipefail

KUBECTL="${KUBECTL:-kubectl}"
CTX="${KUBECONTEXT:-kind-stratt-dev}"
NS="${STRATT_NS:-stratt}"
PRINCIPAL="${STRATT_PRINCIPAL:-bootstrap-admin}"
WORKFLOW="${STRATT_WORKFLOW:-genesis-authz-deploy}"
LPORT="${STRATT_LPORT:-18080}"
ROOT="http://127.0.0.1:${LPORT}"
API="${ROOT}/api/v1"

kc() { "$KUBECTL" --context "$CTX" "$@"; }
api() { # api METHOD PATH  — always as the bootstrap-admin Principal (dev header)
    curl -fsS -X "$1" "${API}$2" -H "X-Stratt-Principal: ${PRINCIPAL}" -H "Content-Type: application/json" "${@:3}"
}

echo "genesis-selfdeploy: port-forward svc/stratt ${LPORT}->8080 (ns ${NS})"
kc -n "$NS" port-forward svc/stratt "${LPORT}:8080" >/dev/null 2>&1 &
PF_PID=$!
trap 'kill "$PF_PID" 2>/dev/null || true' EXIT
# Wait for the forward to answer (/healthz is at the root, not under /api/v1).
for _ in $(seq 1 30); do curl -fsS "${ROOT}/healthz" >/dev/null 2>&1 && break; sleep 1; done

echo "genesis-selfdeploy: launch Workflow ${WORKFLOW} as ${PRINCIPAL}"
run_id=$(api POST "/workflows/${WORKFLOW}/runs" | jq -r '.id')
[ -n "$run_id" ] && [ "$run_id" != "null" ] || { echo "FAIL: no WorkflowRun id returned"; exit 1; }
echo "  WorkflowRun ${run_id}"

echo "genesis-selfdeploy: awaiting the approval Gate…"
gate_id=""
for _ in $(seq 1 30); do
    gate_id=$(api GET "/gates?status=pending" | jq -r --arg r "$run_id" '.[]? | select(.workflowRunId==$r) | .id' | head -1)
    [ -n "$gate_id" ] && [ "$gate_id" != "null" ] && break
    sleep 1
done
[ -n "$gate_id" ] && [ "$gate_id" != "null" ] || { echo "FAIL: no pending Gate for run ${run_id}"; exit 1; }
echo "  approving Gate ${gate_id} as ${PRINCIPAL} (platform-admins)"
api POST "/gates/${gate_id}/decision" --data '{"approve":true}' >/dev/null

echo "genesis-selfdeploy: awaiting WorkflowRun convergence…"
status=""
for _ in $(seq 1 60); do
    status=$(api GET "/workflow-runs/${run_id}" | jq -r '.workflowRun.status // .status // empty')
    case "$status" in
        succeeded) echo "  WorkflowRun succeeded"; break ;;
        failed|cancelled) echo "FAIL: WorkflowRun ${status}"; exit 1 ;;
    esac
    sleep 2
done
[ "$status" = "succeeded" ] || { echo "FAIL: WorkflowRun did not converge (last=${status:-none})"; exit 1; }

echo "genesis-selfdeploy: confirm Stratt materialized the OpenFGA workload"
kc -n stratt-authz rollout status deploy/openfga --timeout=120s
kc -n stratt-authz get deploy,svc -l app.kubernetes.io/name=openfga
echo "genesis-selfdeploy: DONE — Stratt self-deployed a real service from the genesis floor."
