#!/usr/bin/env bash
# run.sh — the turnkey runner for the "EC2: provision a real instance" demo (ADR-0116 D2). Drives real
# provisioning + a live read-model on one substrate: a gated Workflow provisions a REAL EC2 instance
# through the awsec2/create-vm Action against floci (a real-host EC2 backend, ADR-0093), then the awsec2
# Syncer's OBSERVE picks the new instance up into the provisioned-instances View — the build→observe closure.
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
# The View the estate actually declares. Named once: the runner and the estate disagreeing
# on this string is exactly how the observe check came to read a View that does not exist.
OBSERVE_VIEW="provisioned-instances"
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

echo "demo: EC2 is empty to start — ${OBSERVE_VIEW} View: $(count "$OBSERVE_VIEW") instances"

# ── Wait for the awsec2 Actuator to be DISPATCHABLE (status.enabled), not merely declared ─────────
# The reconcile controller declares the Actuator from the staged estate, then RunActuators dials
# stratt-awsec2 and registers its Actions on its own cadence (ADR-0103, no restart). The Workflow
# reconciling is NOT the same fact: this demo launched as soon as the Workflow existed and failed
# with `no action registered as "awsec2/create-vm"` — after the gate, which is the worst place for
# a race to surface. /actuators/{name} reports the live registry status (§1.8), so gate on it.
# The Actuator wait is `dev:await-actuators` in the Taskfile, run before this script — see the note
# in plugins/helm/demo/run.sh for why the private loop that stood here was a silent-death hazard.

# The instance identity is SUPPLIED AT LAUNCH, not baked into the Workflow (ADR-0120 D2). This
# demo has no Intent/Compute — it drives the build Workflow directly — so it plays the part the
# provisioning reconcile plays in the reference estate: it names which instance to build and the
# labels the projection must carry, INCLUDING the stratt.intent/instance correlation label. Before
# ADR-0120 the Workflow hardcoded web-01, so this demo could only ever build one instance and the
# hardcoding was invisible from here.
# NO `ordinal` IN THE BODY BELOW, and it was there until 2026-08-01. The launch had been returning
# HTTP 400 for as long as nobody re-ran this demo: the Workflow declares its inputs with
# `additionalProperties: false` and the properties are instance/projectKind/labels/placement/params.
# `ordinal` appears only inside a DESCRIPTION string ("namePrefix + ordinal"), never as a property,
# so a reader skimming the schema sees the word and assumes the field.
#
# Found by `task e2e:live` on its FIRST run. That is the whole argument for E2E-1 in one defect:
# nothing else in the repo launches this Workflow, so when its contract tightened the only caller
# went stale and every tracker kept saying this demo was live-verified.
INSTANCE="${STRATT_DEMO_INSTANCE:-web-01}"
echo "demo: launch Workflow ${WORKFLOW} as ${PRINCIPAL} (provision ${INSTANCE})"
launch_body=$(jq -nc --arg i "$INSTANCE" '{
  inputs: {
    instance: $i,
    projectKind: "host",
    labels: { fleet: "web", "stratt.intent/instance": $i },
    params: { region: "us-east-1", instanceType: "t3.micro", ami: "ami-0linuxbaseline000" },
    # Placement, EXPLICITLY EMPTY rather than omitted. The Workflow forwards
    # {{.launch.placement.subnet}} — identical to the shipped compute-build, which is the
    # point of converging them — and the substituter refuses an unknown field outright
    # (ADR-0083 D5): it does not treat an absent parent as an empty leaf. So omitting
    # placement fails the Run at ResolveActionStepParams with "template references unknown
    # field", which is exactly what it did once the two copies were converged and nobody
    # re-ran this demo. Empty strings are what an UNPLACED instance means, and the Action
    # skips the axis for each (ADR-0123 D2).
    placement: { subnet: "", subnetRef: "", availabilityZone: "" }
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
    seen=$(count "$OBSERVE_VIEW")
    [ "$seen" -gt 0 ] 2>/dev/null && break
    sleep 3
done
if [ "$seen" -gt 0 ] 2>/dev/null; then
    echo "  the instance now appears in the ${OBSERVE_VIEW} View — real provision, live read-model"
else
    # NOT a soft note any more — this is a FAILURE, and the reason it was not is worth keeping.
    #
    # `task e2e:live` reported a green run ending `ec2-instances: 0`. The first diagnosis was that
    # this demo has no Syncer (no Source and no Connector in its estate) and therefore could not
    # observe. THAT WAS WRONG. The Syncer is real and enabled — `STRATT_AWS_INTERVAL: 15s` in
    # values-demo-ec2.yaml turns on strattd's opt-in instance Syncer — it is simply configured by
    # host env rather than by an estate declaration, so looking only in estate/ found nothing and
    # produced a confident wrong answer.
    #
    # The actual defect: this runner counted a View named `ec2-instances`, and the estate declares
    # `provisioned-instances`. `count()` maps the 404 to 0, so the check could NEVER see an
    # instance — it reported "0 to start" (true by accident) and "0 at the end" (a missing View,
    # not an empty one), and the soft-pass branch turned that into a green run.
    #
    # Two lessons, both already paid for elsewhere on this branch: a name nobody resolves is not a
    # zero, and a check whose failure branch prints prose instead of exiting is not a check.
    echo "FAIL: the instance was built but never observed into view:${OBSERVE_VIEW}."
    echo "  The build→observe closure is what this demo exists to prove — a green run that observed"
    echo "  nothing is the vacuous pass this repo keeps finding. Check the Syncer is enabled"
    echo "  (STRATT_AWS_INTERVAL in values-demo-ec2.yaml) and that ${OBSERVE_VIEW} is declared."
    exit 1
fi

echo
echo "demo: DONE — Stratt provisioned a real EC2 instance through a gated Workflow (fidelity: ${fidelity})."
printf "  Final graph: %s: %s\n" "$OBSERVE_VIEW" "$(count "$OBSERVE_VIEW")"
echo "  Explore the graph in the UI:  (cd ui && npm run dev)  → Views → provisioned-instances"
echo "  Watch the descent:            UI → Runs → WorkflowRun ${run_id}"
echo "  Clean up:       task demo:ec2-only:down   (or full teardown: task dev:kind:down && task dev:down)"
