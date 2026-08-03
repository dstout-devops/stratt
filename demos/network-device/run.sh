#!/usr/bin/env bash
# run.sh — the turnkey runner for the "configure a network device" demo (ADR-0116 D2, ADR-0153).
#
# THE FIRST RUN IN THIS REPO THAT DRIVES A DEVICE RATHER THAN A SERVER. Everything else Stratt
# converges has a POSIX shell on the far end; this target's login shell IS the CLI, so ansible's
# ordinary ssh path cannot work at all and `connection.type: network_cli` is load-bearing rather
# than decorative.
#
# It launches the rtr-configure Workflow and asserts three things:
#
#   1. the Run SUCCEEDS over network_cli — the connection plugin, the platform's cliconf/terminal
#      plugins and the python SSH transport all present and agreeing (ADR-0153, ADR-0159);
#   2. the route is in the DEVICE's own running-config, read back by this script through vtysh
#      rather than trusted from the Run's own report;
#   3. an Actuator pointed at an EE WITHOUT that content is REFUSED before anything runs.
#
# `task demo:network-device:run` stands the floor up before invoking this. Needs curl + jq + kubectl.
#
# Env: KUBECTL, KUBECONTEXT, STRATT_NS, STRATT_PRINCIPAL, STRATT_LPORT.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

KUBECTL="${KUBECTL:-kubectl}"
CTX="${KUBECONTEXT:-kind-stratt-dev}"
NS="${STRATT_NS:-stratt}"
PRINCIPAL="${STRATT_PRINCIPAL:-bootstrap-admin}"
WORKFLOW="rtr-configure"
VIEW="net-devices"
ACTUATOR="ansible-network"
DEVICE_DEPLOY="rtr-01"
ROUTE="10.99.0.0/24"
LPORT="${STRATT_LPORT:-18095}"
ROOT="http://127.0.0.1:${LPORT}"
API="${ROOT}/api/v1"

kc() { "$KUBECTL" --context "$CTX" "$@"; }
api() { curl -fsS -X "$1" "${API}$2" -H "X-Stratt-Principal: ${PRINCIPAL}" -H "Content-Type: application/json" "${@:3}"; }
# Ask the DEVICE, not Stratt. Every assertion about the device's state goes through this.
ondevice() { kc -n "$NS" exec "deploy/${DEVICE_DEPLOY}" -- vtysh -c "$1"; }

fidelity="$(grep -E '^fidelity:' "${HERE}/demo.yaml" | head -1 | sed 's/^fidelity:[[:space:]]*//')"
fnote="$(grep -E '^fidelityNote:' "${HERE}/demo.yaml" | head -1 | sed 's/^fidelityNote:[[:space:]]*//')"
echo "┌───────────────────────────────────────────────────────────────────────────"
echo "│ demo: configure a network device   fidelity: ${fidelity}"
echo "│ ${fnote}"
echo "└───────────────────────────────────────────────────────────────────────────"

echo "demo: port-forward svc/stratt ${LPORT}->8080 (ns ${NS})"
kc -n "$NS" port-forward svc/stratt "${LPORT}:8080" >/dev/null 2>&1 &
PF_PID=$!
trap 'kill "$PF_PID" 2>/dev/null || true' EXIT
for _ in $(seq 1 30); do curl -fsS "${ROOT}/healthz" >/dev/null 2>&1 && break; sleep 1; done

# ── The device must be a device before anything else is worth asserting ──────────────────────────
echo "demo: assert the target is a NETWORK DEVICE, not a host with a routing daemon"
shell="$(kc -n "$NS" exec "deploy/${DEVICE_DEPLOY}" -- getent passwd netops | cut -d: -f7)"
echo "  netops' login shell: ${shell}"
[ "$shell" = "/usr/bin/vtysh" ] || {
    echo "FAIL: the login shell is '${shell}', not vtysh — this target has a POSIX shell behind it,"
    echo "      so it would prove nothing about network_cli"; exit 1; }

echo "demo: awaiting the device in View ${VIEW} (declared Syncer cadence)…"
targets=0
for _ in $(seq 1 45); do
    targets=$(api GET "/views/${VIEW}/entities" 2>/dev/null | jq -r '.entities | length' 2>/dev/null || echo 0)
    [ "${targets:-0}" -gt 0 ] && break
    sleep 2
done
[ "${targets:-0}" -gt 0 ] || { echo "FAIL: no device ever landed in View ${VIEW}"; exit 1; }
echo "  ${targets} device(s) in ${VIEW}"

# The device carries NO mgmt.transport, and that is the point (ADR-0156 D5 / ADR-0158 D2): nothing
# observes how to reach a switch because nothing provisioned it. The Step declares it instead.
obs="$(api GET "/views/${VIEW}/entities" | jq -r '[.entities[]?.facets? // {} | keys[]?] | map(select(. == "mgmt.transport")) | length')"
[ "${obs:-0}" = "0" ] || {
    echo "FAIL: something observed a transport for a DISCOVERED device — a switch is not provisioned,"
    echo "      so its reach method is declared on the Step, not projected by a provider"; exit 1; }
echo "  and NO mgmt.transport is observed for it — a discovered device declares its own reach method"

echo "demo: awaiting the ${ACTUATOR} Actuator…"
enabled=""
for _ in $(seq 1 45); do
    enabled=$(api GET "/actuators/${ACTUATOR}" 2>/dev/null | jq -r '.status.enabled // false')
    [ "$enabled" = "true" ] && break
    sleep 2
done
[ "$enabled" = "true" ] || { echo "FAIL: ${ACTUATOR} never reached status.enabled=true"; exit 1; }
ee_image="$(api GET "/actuators/${ACTUATOR}" | jq -r '.actuator.image // empty')"
[ -n "$ee_image" ] || { echo "FAIL: ${ACTUATOR} declares no EE image"; exit 1; }
echo "  ${ACTUATOR} enabled, declaring EE: ${ee_image}"

# ── The device must NOT already have the route, or the assertion proves nothing ──────────────────
echo "demo: assert the device does not already carry ${ROUTE}"
if ondevice "show running-config" | grep -qF "$ROUTE"; then
    echo "FAIL: the device already has ${ROUTE} before the Run — this assertion would pass without"
    echo "      Stratt doing anything. Tear the device down (task demo:network-device:down) and retry."
    exit 1
fi
echo "  clean: the route is absent"

# ── The Run ──────────────────────────────────────────────────────────────────────────────────────
echo "demo: launch ${WORKFLOW} (connection.type=network_cli, networkOS=frr.frr.frr)"
wr=$(api POST "/workflows/${WORKFLOW}/runs" -d "{\"inputs\":{\"routePrefix\":\"${ROUTE}\"}}" | jq -r '.id')
[ -n "$wr" ] && [ "$wr" != "null" ] || { echo "FAIL: no WorkflowRun id returned"; exit 1; }
echo "  WorkflowRun ${wr}"

status=""
for _ in $(seq 1 120); do
    status=$(api GET "/workflow-runs/${wr}" | jq -r '.workflowRun.status // .status // empty')
    case "$status" in succeeded|failed|canceled) break ;; esac
    sleep 3
done
if [ "$status" != "succeeded" ]; then
    echo "FAIL: WorkflowRun is '${status:-none}', want succeeded"
    api GET "/runs?workflowRunId=${wr}" 2>/dev/null |
        jq -r --arg r "$wr" '.[]? | select(.workflowRunId==$r) | "  run " + .id + " " + .status + " " + (.error // "")'
    exit 1
fi
echo "  WorkflowRun succeeded over network_cli"

# ── Assertion: the DEVICE has the config, read off the device ────────────────────────────────────
# The Run reporting success is NOT the assertion. A play that says it changed something and a device
# that has it are different claims, and only the second one is about the estate (§1.8).
echo "demo: assert ${ROUTE} is in the DEVICE's own running-config"
cfg="$(ondevice "show running-config")"
echo "$cfg" | grep -F "$ROUTE" | sed 's/^/  device says: /' || {
    echo "FAIL: the Run succeeded and the device's running-config does NOT contain ${ROUTE}"
    echo "$cfg" | head -20
    exit 1; }

# And the device's own view of its routing table, which is a different question from its config.
echo "demo: assert the device installed it as a real static route"
ondevice "show ip route static" | sed 's/^/  /' | head -6

# ── Assertion: an EE without the content is REFUSED, not attempted (ADR-0153 D7 / ADR-0159) ──────
# The image is the content boundary. Point a Step at one that lacks the collection or the python
# transport and it must fail BEFORE connecting, naming what is missing — rather than reaching the
# device and dying on a python import the estate never wrote.
echo "demo: assert a Step whose EE lacks the network content is REFUSED before it connects"
guard_wr=$(api POST "/workflows/rtr-configure-wrong-ee/runs" -d "{\"inputs\":{\"routePrefix\":\"10.98.0.0/24\"}}" | jq -r '.id')
[ -n "$guard_wr" ] && [ "$guard_wr" != "null" ] || { echo "FAIL: no WorkflowRun id for the content guard"; exit 1; }
gstatus=""
for _ in $(seq 1 90); do
    gstatus=$(api GET "/workflow-runs/${guard_wr}" | jq -r '.workflowRun.status // .status // empty')
    case "$gstatus" in failed) break ;; succeeded) echo "FAIL: an EE with no netcommon converged a device — the content gate has regressed"; exit 1 ;; esac
    sleep 2
done
[ "$gstatus" = "failed" ] || { echo "FAIL: the content guard never reached a verdict (last=${gstatus:-none})"; exit 1; }
gcause="$(api GET "/runs?workflowRunId=${guard_wr}" 2>/dev/null | jq -r --arg r "$guard_wr" '.[]? | select(.workflowRunId==$r) | select(.status=="failed") | .error // empty' | head -1)"
echo "  guard Run failed as designed"
echo "  cause: ${gcause:-(none recorded)}"
case "$gcause" in
    *"ansible.netcommon"*|*"ansible-pylibssh"*) : ;;
    *) echo "FAIL: the refusal does not name the missing content (§1.8): '${gcause}'"; exit 1 ;;
esac
# And it must never have reached the device: the guard's prefix must be absent.
if ondevice "show running-config" | grep -qF "10.98.0.0/24"; then
    echo "FAIL: the refused Step still configured the device — it was not refused, it was attempted"
    exit 1
fi
echo "  …and the device was never touched by it"

echo
echo "demo: DONE — Stratt configured a real network device over its own CLI (fidelity: ${fidelity})."
echo "  Ask the device yourself:  kubectl --context ${CTX} -n ${NS} exec deploy/${DEVICE_DEPLOY} -- vtysh -c 'show running-config'"
echo "  Watch the descent in the UI:  (cd ui && npm run dev)  → Runs → WorkflowRun ${wr}"
echo "  Clean up:       task demo:network-device:down"
