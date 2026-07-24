#!/usr/bin/env bash
# run.sh — the turnkey runner for the "k8s: deploy an app" demo (ADR-0116 D2). Drives the descent on a
# Stratt floor whose desired state IS this demo's estate (staged into the declarations mount + enforced
# by the reconcile controller — actuators/credential-refs are CaC-only, §2.2/2.3, so the estate is
# delivered as CaC, never `stratt apply`). It launches the gated deploy-hello Workflow, approves the
# platform-admins gate as the dev-header bootstrap-admin Principal, waits for the WorkflowRun to
# converge, and ASSERTS the real `hello` Deployment is Ready — the proven genesis-selfdeploy loop.
#
# The `task demo:k8s-deploy:run` target stands the floor up (kind + strattd + the helm plugin + this
# estate) before invoking this script. Needs curl + jq + kubectl.
#
# Env: KUBECTL, KUBECONTEXT, STRATT_NS, STRATT_PRINCIPAL, STRATT_LPORT.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_REPO="$(cd "${HERE}/../.." && pwd)"

KUBECTL="${KUBECTL:-kubectl}"
CTX="${KUBECONTEXT:-kind-stratt-dev}"
NS="${STRATT_NS:-stratt}"
PRINCIPAL="${STRATT_PRINCIPAL:-bootstrap-admin}"
WORKFLOW="deploy-hello"
DEMO_NS="demo"
RELEASE="hello"
LPORT="${STRATT_LPORT:-18081}"
ROOT="http://127.0.0.1:${LPORT}"
API="${ROOT}/api/v1"

kc() { "$KUBECTL" --context "$CTX" "$@"; }
api() { # api METHOD PATH  — always as the bootstrap-admin Principal (dev header)
    curl -fsS -X "$1" "${API}$2" -H "X-Stratt-Principal: ${PRINCIPAL}" -H "Content-Type: application/json" "${@:3}"
}

# ── Surface the declared fidelity up front (D3: never present simulated as real) ─────────────────
fidelity="$(grep -E '^fidelity:' "${HERE}/demo.yaml" | head -1 | sed 's/^fidelity:[[:space:]]*//')"
fnote="$(grep -E '^fidelityNote:' "${HERE}/demo.yaml" | head -1 | sed 's/^fidelityNote:[[:space:]]*//')"
echo "┌───────────────────────────────────────────────────────────────────────────"
echo "│ demo: k8s — deploy an app        fidelity: ${fidelity}"
echo "│ ${fnote}"
echo "└───────────────────────────────────────────────────────────────────────────"

echo "demo: port-forward svc/stratt ${LPORT}->8080 (ns ${NS})"
kc -n "$NS" port-forward svc/stratt "${LPORT}:8080" >/dev/null 2>&1 &
PF_PID=$!
trap 'kill "$PF_PID" 2>/dev/null || true' EXIT
for _ in $(seq 1 30); do curl -fsS "${ROOT}/healthz" >/dev/null 2>&1 && break; sleep 1; done

# ── Wait for the helm Actuator to be DISPATCHABLE (status.enabled), not merely declared ───────────
# The reconcile controller declares the Actuator from the staged estate, then RunActuators dials
# stratt-helm + registers helm/deploy on its cadence (ADR-0103, no restart). GET /actuators lists
# DECLARED actuators immediately, but the deploy Step needs it REGISTERED — /actuators/{name}
# reports the live registry status (§1.8), so gate on status.enabled to avoid the launch race.
echo "demo: awaiting helm/deploy Actuator registration (status.enabled)…"
enabled=""
for _ in $(seq 1 45); do
    enabled=$(api GET "/actuators/helm" 2>/dev/null | jq -r '.status.enabled // false')
    [ "$enabled" = "true" ] && break
    sleep 2
done
[ "$enabled" = "true" ] || { echo "FAIL: helm Actuator never reached status.enabled=true"; exit 1; }
echo "  helm Actuator registered (helm/deploy dispatchable)"

echo "demo: launch Workflow ${WORKFLOW} as ${PRINCIPAL}"
run_id=$(api POST "/workflows/${WORKFLOW}/runs" | jq -r '.id')
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
        succeeded) echo "  WorkflowRun succeeded"; break ;;
        failed|cancelled) echo "FAIL: WorkflowRun ${status}"; exit 1 ;;
    esac
    sleep 2
done
[ "$status" = "succeeded" ] || { echo "FAIL: WorkflowRun did not converge (last=${status:-none})"; exit 1; }

echo "demo: assert the real ${RELEASE} Deployment is Ready in ns/${DEMO_NS}"
kc -n "$DEMO_NS" rollout status "deploy/${RELEASE}" --timeout=120s
kc -n "$DEMO_NS" get deploy,svc -l "app.kubernetes.io/name=${RELEASE}"

echo
echo "demo: DONE — Stratt deployed a real app through a gated Workflow (fidelity: ${fidelity})."
echo "  See the page:   kubectl --context ${CTX} -n ${DEMO_NS} port-forward svc/${RELEASE} 8888:80  # → http://localhost:8888"
echo "  Watch the descent in the UI:  (cd ui && npm run dev)  → Runs → WorkflowRun ${run_id}"
echo "  Clean up:       task demo:k8s-deploy:down   (or full teardown: task dev:kind:down)"
