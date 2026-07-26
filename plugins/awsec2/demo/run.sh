#!/usr/bin/env bash
# run.sh — the turnkey runner for the "EC2: provision a real instance" demo (ADR-0116 D2). Drives real
# provisioning + a live read-model on one substrate: a gated Workflow provisions a REAL EC2 instance
# through the awsec2/create-vm Action against floci (a real-host EC2 backend, ADR-0093), then the awsec2
# Syncer's OBSERVE picks the new instance up into the ec2-instances View — the build→observe closure.
# Unlike the vSphere demo (a pre-seeded read graph), floci starts EMPTY: the graph comes alive WITH the
# instance you build.
#
# `task demo:ec2-only:run` stands the floor up (kind + strattd + the awsec2 plugin + floci + the staged
# CaC estate) before invoking this. Needs curl + jq + kubectl.
#
# Env: KUBECTL, KUBECONTEXT, STRATT_NS, STRATT_PRINCIPAL, STRATT_LPORT.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

KUBECTL="${KUBECTL:-kubectl}"
CTX="${KUBECONTEXT:-kind-stratt-dev}"
NS="${STRATT_NS:-stratt}"
PRINCIPAL="${STRATT_PRINCIPAL:-bootstrap-admin}"
WORKFLOW="compute-build"
LPORT="${STRATT_LPORT:-18092}"
ROOT="http://127.0.0.1:${LPORT}"
API="${ROOT}/api/v1"

kc() { "$KUBECTL" --context "$CTX" "$@"; }
api() { curl -fsS -X "$1" "${API}$2" -H "X-Stratt-Principal: ${PRINCIPAL}" -H "Content-Type: application/json" "${@:3}"; }
count() { api GET "/views/$1/entities" 2>/dev/null | jq -r '.entities | length' 2>/dev/null || echo 0; }

fidelity="$(grep -E '^fidelity:' "${HERE}/demo.yaml" | head -1 | sed 's/^fidelity:[[:space:]]*//')"
echo "┌───────────────────────────────────────────────────────────────────────────"
echo "│ demo: EC2 — provision a real instance through a gated Workflow   fidelity: ${fidelity}"
echo "│ floci runs REAL Docker containers as EC2 instances (ADR-0093) — not a mock."
echo "└───────────────────────────────────────────────────────────────────────────"

echo "demo: port-forward svc/stratt ${LPORT}->8080 (ns ${NS})"
kc -n "$NS" port-forward svc/stratt "${LPORT}:8080" >/dev/null 2>&1 &
PF_PID=$!
trap 'kill "$PF_PID" 2>/dev/null || true' EXIT
for _ in $(seq 1 30); do curl -fsS "${ROOT}/healthz" >/dev/null 2>&1 && break; sleep 1; done

# Wait for the reconcile controller to load the demo estate (the compute-build Workflow) into the
# store — strattd is up before its first reconcile, and floci starts empty so there's no Syncer
# projection to gate on (as the vSphere demo used). Poll until the Workflow is registered.
echo "demo: awaiting the demo estate to reconcile (compute-build Workflow)…"
ready=""
for _ in $(seq 1 40); do
    curl -fsS -o /dev/null "${API}/workflows/${WORKFLOW}" -H "X-Stratt-Principal: ${PRINCIPAL}" 2>/dev/null && { ready=1; break; }
    sleep 2
done
[ -n "$ready" ] || { echo "FAIL: the ${WORKFLOW} Workflow never reconciled into the store"; exit 1; }

echo "demo: EC2 is empty to start — ec2-instances View: $(count ec2-instances) instances"

# The instance identity is SUPPLIED AT LAUNCH, not baked into the Workflow (ADR-0120 D2). This
# demo has no Intent/Compute — it drives the build Workflow directly — so it plays the part the
# provisioning reconcile plays in the reference estate: it names which instance to build and the
# labels the projection must carry, INCLUDING the stratt.intent/instance correlation label. Before
# ADR-0120 the Workflow hardcoded web-01, so this demo could only ever build one instance and the
# hardcoding was invisible from here.
INSTANCE="${STRATT_DEMO_INSTANCE:-web-01}"
echo "demo: launch Workflow ${WORKFLOW} as ${PRINCIPAL} (provision ${INSTANCE})"
launch_body=$(jq -nc --arg i "$INSTANCE" '{
  inputs: {
    instance: $i,
    ordinal: 1,
    projectKind: "host",
    labels: { fleet: "web", "stratt.intent/instance": $i },
    params: { region: "us-east-1", instanceType: "t3.micro", ami: "ami-0linuxbaseline000" }
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
        succeeded) echo "  WorkflowRun succeeded — a real EC2 instance was provisioned"; break ;;
        failed|cancelled)
            echo "FAIL: WorkflowRun ${status}"
            # §1.8 descent: surface the real cause the build reported (the run-error surfacing fix).
            api GET "/runs?workflowRunId=${run_id}" 2>/dev/null | jq -r '.[]? | select(.status=="failed") | "  cause: " + (.error // "(none recorded)")' | head -1
            exit 1 ;;
    esac
    sleep 2
done
[ "$status" = "succeeded" ] || { echo "FAIL: WorkflowRun did not converge (last=${status:-none})"; exit 1; }

echo "demo: awaiting the Syncer to OBSERVE the new instance (build → observe closure)…"
seen=0
for _ in $(seq 1 20); do
    seen=$(count ec2-instances)
    [ "$seen" -gt 0 ] 2>/dev/null && break
    sleep 3
done
if [ "$seen" -gt 0 ] 2>/dev/null; then
    echo "  the instance now appears in the ec2-instances View — real provision, live read-model"
else
    echo "  (instance not yet observed — the Syncer picks it up on its next cycle)"
fi

echo
echo "demo: DONE — Stratt provisioned a real EC2 instance through a gated Workflow (fidelity: ${fidelity})."
printf "  Final graph: ec2-instances: %s\n" "$(count ec2-instances)"
echo "  Explore the graph in the UI:  (cd ui && npm run dev)  → Views → ec2-instances"
echo "  Watch the descent:            UI → Runs → WorkflowRun ${run_id}"
echo "  Clean up:       task demo:ec2-only:down   (or full teardown: task dev:kind:down && task dev:down)"
