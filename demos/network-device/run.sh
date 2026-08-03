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
GROUPED_WORKFLOW="rtr-grouped"
STORM_WORKFLOW="rtr-flap-remediate"
BATCH_WORKFLOW="rtr-batch-remediate"
# The NMS's signing key, created inside OpenBao and never exported (ADR-0164 D2).
SIGNING_KEY="nms-webhook"
BAOPORT="${STRATT_BAO_LPORT:-18200}"
BAO_TOKEN="${STRATT_BAO_TOKEN:-stratt-dev-root}"
# The plaintext half of the Emitter's declared tokenHash (estate/emitters/link-flaps.yaml). It is in
# the open on purpose: it authenticates one POST to one throwaway kind cluster and grants nothing
# else. A demo must never teach a reader to commit a token that means something.
FLAP_TOKEN="network-device-demo-not-a-secret"
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

# ── Assertion: a play targeting a GROUP reaches it, and group_vars load (ADR-0161) ───────────────
# The shape of essentially every real Ansible playbook, and the one Stratt could not run at all until
# ADR-0161: `hosts: tier_edge` rather than `hosts: all`. Before it, buildInventory wrote [all], the
# hosts and [all:vars] — the whole file — so a group pattern matched NOTHING and
# group_vars/tier_edge.yml was never loaded, because ansible keys those files on inventory groups.
#
# Nothing in this estate enumerates `edge`. The device carries `tier: edge` as a LABEL and the group
# follows from `groupBy`, which is keyed_groups' generative behaviour arriving as data.
echo "demo: assert a play targeting a GROUP converges the device, with group_vars loaded"
gwr=$(api POST "/workflows/${GROUPED_WORKFLOW}/runs" | jq -r '.id')
[ -n "$gwr" ] && [ "$gwr" != "null" ] || { echo "FAIL: no WorkflowRun id for the grouped Step"; exit 1; }
gstat=""
for _ in $(seq 1 120); do
    gstat=$(api GET "/workflow-runs/${gwr}" | jq -r '.workflowRun.status // .status // empty')
    case "$gstat" in succeeded|failed|canceled) break ;; esac
    sleep 3
done
if [ "$gstat" != "succeeded" ]; then
    echo "FAIL: the grouped Run is '${gstat:-none}', want succeeded."
    echo "      A play saying 'hosts: tier_edge' that matches nothing is the pre-ADR-0161 behaviour:"
    echo "      ansible reports no hosts matched and the vacuous-run guard fails the Run."
    api GET "/runs?workflowRunId=${gwr}" 2>/dev/null |
        jq -r --arg r "$gwr" '.[]? | select(.workflowRunId==$r) | "  run " + .id + " " + .status + " " + (.error // "")'
    exit 1
fi
echo "  the grouped Run succeeded — [tier_edge] existed and matched"

# The inventory the Run actually rendered, read off its own event stream (§1.8: the shim emits it as
# kind=inventory). Asserting the SECTION rather than trusting the Run's success, because a play can
# succeed for reasons that have nothing to do with the group.
grun=$(api GET "/runs?workflowRunId=${gwr}" | jq -r --arg r "$gwr" '.[]? | select(.workflowRunId==$r) | .id' | head -1)
ginv=$(kc -n "$NS" logs "job-name=stratt-run-${grun}-s0" --tail=-1 2>/dev/null ||
       kc -n "$NS" logs -l "job-name=stratt-run-${grun}-s0" --tail=-1 2>/dev/null || true)
case "$ginv" in
    *"[tier_edge]"*) echo "  the rendered inventory carried a [tier_edge] section" ;;
    *) echo "FAIL: the rendered inventory has no [tier_edge] section — the group was not rendered"; exit 1 ;;
esac
case "$ginv" in
    *"loaded-from-group_vars"*) echo "  …and group_vars/tier_edge.yml LOADED — the file ADR-0134 promised and nothing could read" ;;
    *) echo "FAIL: group_vars did not load; the play's assert would have failed"; exit 1 ;;
esac

# ── Assertion: a BURST of events launches exactly ONE Run (ADR-0162) ─────────────────────────────
# One link flap is noise; five in ten minutes is an incident. Before ADR-0162 a Trigger could only
# say "when THIS event arrives, do that", so a flapping interface launched a Run per flap and the
# automation amplified the incident it existed to settle.
#
# EVERY NUMBER BELOW IS COUNTED FROM THE RUNS THE ESTATE ACTUALLY HAS, never from the engine's own
# log. "The storm was damped" is exactly the class of claim this repo has repeatedly found false when
# executed, and a log line saying "accumulating" would be true whether or not a Run was launched.
echo "demo: post a burst of link-flap events and assert the storm is damped to ONE Run"

# Each demo run is its OWN burst, and the Trigger correlates on it. `down` deliberately leaves the
# floor standing, so a second run inherits the first one's Postgres — without this, a re-run inside
# the ten-minute window would fire on its first flap, having inherited a count it did not earn.
BURST="burst-$(date +%s)-$$"
flap() {
    curl -fsS -o /dev/null -w '%{http_code}' -X POST "${ROOT}/emitters/link-flaps" \
        -H "X-Stratt-Emitter-Token: ${FLAP_TOKEN}" -H "Content-Type: application/json" \
        -d "{\"alertname\":\"LinkFlap\",\"device\":\"rtr-01\",\"burst\":\"${BURST}\",\"seq\":$1}"
}
# Runs of the remediation Workflow, right now. It is launched by the Trigger and by nothing else in
# this demo, so this number IS the number of times the engine decided a storm had happened.
storm_runs() {
    api GET "/workflow-runs?limit=500" | jq --arg w "$STORM_WORKFLOW" '[.[]? | select(.workflowName==$w)] | length'
}

# COUNTED AS A DELTA against what the floor already had. Asserting an absolute zero would make this
# demo pass only on a never-used cluster, and what is actually being claimed is what THIS burst
# caused — which is the honest measurement anyway.
base="$(storm_runs)"
echo "  (${base} ${STORM_WORKFLOW} Run(s) already on this floor; counting the delta)"
# The marker must be ABSENT first, or the "the device was remediated" assertion below passes on a
# previous run's evidence. The device is recreated by demo:network-device:down + :run.
if ondevice "show running-config" | grep -qF "10.97.0.0/24"; then
    echo "FAIL: the device already carries the storm's marker route — this assertion would pass"
    echo "      without the Trigger doing anything. task demo:network-device:down, then retry."
    exit 1
fi

# Four flaps. Each MATCHES the Trigger's `when` — the CEL is true every time — and none of them may
# launch anything, because the estate asked to be told about storms, not flaps.
for i in 1 2 3 4; do
    code="$(flap "$i")"
    [ "$code" = "202" ] || { echo "FAIL: flap ${i} was not accepted (HTTP ${code})"; exit 1; }
done
sleep 5
mid=$(( $(storm_runs) - base ))
[ "${mid:-0}" = "0" ] || {
    echo "FAIL: ${mid} Run(s) launched from four flaps — the threshold is not being counted, so"
    echo "      every flap of a storm becomes a Run and the automation amplifies the incident"
    exit 1; }
echo "  four flaps matched the rule and launched NOTHING — below the threshold"

# The fifth crosses it.
code="$(flap 5)"; [ "$code" = "202" ] || { echo "FAIL: flap 5 was not accepted (HTTP ${code})"; exit 1; }
after=0
for _ in $(seq 1 30); do
    after=$(( $(storm_runs) - base ))
    [ "${after:-0}" -ge 1 ] && break
    sleep 2
done
[ "${after:-0}" -ge 1 ] || { echo "FAIL: the fifth flap did not launch the remediation Workflow"; exit 1; }
echo "  the fifth flap launched it — the storm was recognised"

# ── And the window RESET rather than slid, which is the whole design of D3 ───────────────────────
# A sliding window fires again on the 6th, 7th and 8th event: one storm becomes a storm of Runs,
# which is the problem being solved rather than a side effect of solving it.
for i in 6 7 8 9; do
    code="$(flap "$i")"; [ "$code" = "202" ] || { echo "FAIL: flap ${i} was not accepted (HTTP ${code})"; exit 1; }
done
sleep 8
total=$(( $(storm_runs) - base ))
[ "${total:-0}" = "1" ] || {
    echo "FAIL: ${total} Runs after nine flaps, want exactly 1 — the window slid instead of resetting,"
    echo "      so a sustained storm produces a Run per event past the threshold"
    exit 1; }
echo "  nine flaps in total, and still exactly ONE Run — the window reset, it did not slide"

# The Run has to have actually converged the device. A damped storm that launched a Run which did
# nothing would satisfy every count above and remediate nothing.
# Newest first, so this is THIS burst's Run rather than one an earlier run left behind.
swr="$(api GET "/workflow-runs?limit=500" | jq -r --arg w "$STORM_WORKFLOW" '[.[]? | select(.workflowName==$w)][0].id')"
sstat=""
for _ in $(seq 1 120); do
    sstat=$(api GET "/workflow-runs/${swr}" | jq -r '.workflowRun.status // .status // empty')
    case "$sstat" in succeeded|failed|canceled) break ;; esac
    sleep 3
done
[ "$sstat" = "succeeded" ] || { echo "FAIL: the storm's Run is '${sstat:-none}', want succeeded"; exit 1; }
ondevice "show running-config" | grep -qF "10.97.0.0/24" || {
    echo "FAIL: the remediation Run succeeded but its route is not in the DEVICE's running-config"
    exit 1; }
echo "  …and the device's own running-config carries the remediation route 10.97.0.0/24"

# ── Assertion: ONE POST, five events, one Run — a shape core has never heard of (ADR-0163) ───────
# Before ADR-0163 the ingest surface could fan out exactly one payload shape, Alertmanager's, and it
# knew it by having that vendor's field names compiled into the Go control plane. The body below was
# invented for this demo and no Go was written for it: `estate/emitters/nms-batch.yaml` says where
# the items are and which envelope fields to fold in.
#
# THE ASSERTION FAILS IN BOTH DIRECTIONS, which is why it is worth running. If the fan-out does not
# happen, this POST is ONE event, `count: 5` is never reached and NO Run exists. If it over-fans,
# more than one Run does.
echo "demo: post ONE batched report in a shape core has never heard of, and assert it fans out"

batch_runs() {
    api GET "/workflow-runs?limit=500" | jq --arg w "$BATCH_WORKFLOW" '[.[]? | select(.workflowName==$w)] | length'
}
bbase="$(batch_runs)"
echo "  (${bbase} ${BATCH_WORKFLOW} Run(s) already on this floor; counting the delta)"
if ondevice "show running-config" | grep -qF "10.96.0.0/24"; then
    echo "FAIL: the device already carries the batch's marker route — see above. Tear down and retry."
    exit 1
fi
BATCH_ID="batch-$(date +%s)-$$"

# ── The NMS SIGNS what it sends, and Stratt checks it against a key it never holds (ADR-0164) ────
#
# THE KEY IS CREATED INSIDE OPENBAO AND NEVER EXPORTED. This script plays the NMS, so it asks
# OpenBao for the MAC of its own body — which is exactly what a real source does with its copy of
# the shared secret. What matters is the other end: the control plane verifies by asking the plugin
# that holds the key, because §2.5 forbids it from holding one itself.
echo "demo: seed the NMS signing key inside OpenBao (the control plane never reads it)"
kc -n "$NS" port-forward svc/openbao "${BAOPORT}:8200" >/dev/null 2>&1 &
BAO_PF=$!
trap 'kill "$PF_PID" "$BAO_PF" 2>/dev/null || true' EXIT
for _ in $(seq 1 30); do curl -fsS "http://127.0.0.1:${BAOPORT}/v1/sys/health" >/dev/null 2>&1 && break; sleep 1; done
bao() { curl -fsS -H "X-Vault-Token: ${BAO_TOKEN}" "$@"; }
# The Transit engine is not mounted in a dev OpenBao — mount it, idempotently.
bao -X POST "http://127.0.0.1:${BAOPORT}/v1/sys/mounts/transit" -d '{"type":"transit"}' >/dev/null 2>&1 || true
# exportable=false is the assertion, not decoration: nothing — including strattd — can read this
# key back out of OpenBao. Verification therefore cannot be happening anywhere but inside it.
bao -X POST "http://127.0.0.1:${BAOPORT}/v1/transit/keys/${SIGNING_KEY}" \
    -d '{"exportable":false}' >/dev/null
echo "  transit key ${SIGNING_KEY} exists, and exportable=false — nothing can read it back out"

# sign returns the hex MAC OpenBao computes over exactly these bytes.
sign() {
    local b64 out
    b64="$(printf '%s' "$1" | base64 -w0)"
    out="$(bao -X POST "http://127.0.0.1:${BAOPORT}/v1/transit/hmac/${SIGNING_KEY}/sha2-256" \
        -d "{\"input\":\"${b64}\"}" | jq -r '.data.hmac')"
    # "vault:v1:<base64>" — the version prefix is Transit's, not the source's.
    printf '%s' "${out##*:}" | base64 -d | od -An -tx1 | tr -d ' \n'
}

# One POST. Five link events, nested under report.linkEvents; `site` merged from the envelope, and
# the envelope's `status` merged as `batchStatus` because every event carries a `status` of its own
# — the collision ADR-0163 D3 refuses to resolve silently.
BATCH_BODY='{
      "status":"open",
      "report":{
        "site":"lab-1",
        "batchId":"'"${BATCH_ID}"'",
        "linkEvents":[
          {"kind":"link.flap","port":"ge-0/0/1","status":"down"},
          {"kind":"link.flap","port":"ge-0/0/2","status":"down"},
          {"kind":"link.flap","port":"ge-0/0/3","status":"down"},
          {"kind":"link.flap","port":"ge-0/0/4","status":"down"},
          {"kind":"link.flap","port":"ge-0/0/5","status":"down"}
        ]}}'

post_batch() {
    curl -fsS -o /dev/null -w '%{http_code}' -X POST "${ROOT}/emitters/nms-batch" \
        -H "X-Stratt-Emitter-Token: ${FLAP_TOKEN}" -H "Content-Type: application/json" \
        -H "X-NMS-Signature: sha256=$2" --data-binary "$1" || true
}

# ── Refused first, so acceptance means something ─────────────────────────────────────────────────
# A signature over DIFFERENT bytes must not authenticate these ones. This is the assertion that
# makes the accepted case evidence rather than a coincidence: without it, a verifier that returned
# true unconditionally would pass every other check in this section.
tampered="${BATCH_BODY/lab-1/lab-9}"
code="$(post_batch "$BATCH_BODY" "$(sign "$tampered")")"
[ "$code" = "401" ] || {
    echo "FAIL: a signature over different bytes was accepted (HTTP ${code}) — the body is not"
    echo "      actually being verified, or it is being verified after something reshaped it"
    exit 1; }
echo "  a signature over different bytes is REFUSED (401)"

code="$(post_batch "$BATCH_BODY" "$(sign "$BATCH_BODY")")"
[ "$code" = "202" ] || { echo "FAIL: the correctly signed report was not accepted (HTTP ${code})"; exit 1; }
echo "  the correctly signed report is accepted — verified against a key strattd never read"

after=0
for _ in $(seq 1 30); do
    after=$(( $(batch_runs) - bbase ))
    [ "${after:-0}" -ge 1 ] && break
    sleep 2
done
[ "${after:-0}" -ge 1 ] || {
    echo "FAIL: one POST carrying five nested link events launched nothing."
    echo "      That is what NO fan-out looks like: the body arrived as a single event, so a"
    echo "      Trigger asking for five never saw more than one."
    exit 1; }
sleep 5
total=$(( $(batch_runs) - bbase ))
[ "${total:-0}" = "1" ] || { echo "FAIL: ${total} Runs from one POST, want exactly 1"; exit 1; }
echo "  one POST became five events and crossed the threshold — exactly ONE Run"

bwr="$(api GET "/workflow-runs?limit=500" | jq -r --arg w "$BATCH_WORKFLOW" '[.[]? | select(.workflowName==$w)][0].id')"
bstat=""
for _ in $(seq 1 120); do
    bstat=$(api GET "/workflow-runs/${bwr}" | jq -r '.workflowRun.status // .status // empty')
    case "$bstat" in succeeded|failed|canceled) break ;; esac
    sleep 3
done
[ "$bstat" = "succeeded" ] || { echo "FAIL: the batch Run is '${bstat:-none}', want succeeded"; exit 1; }
ondevice "show running-config" | grep -qF "10.96.0.0/24" || {
    echo "FAIL: the batch Run succeeded but its own marker route is not on the device"; exit 1; }
echo "  …and its own marker route 10.96.0.0/24 is on the device — its own evidence, not the storm's"

echo
echo "demo: DONE — Stratt configured a real network device over its own CLI (fidelity: ${fidelity})."
echo "  Ask the device yourself:  kubectl --context ${CTX} -n ${NS} exec deploy/${DEVICE_DEPLOY} -- vtysh -c 'show running-config'"
echo "  Watch the descent in the UI:  (cd ui && npm run dev)  → Runs → WorkflowRun ${wr}"
echo "  Clean up:       task demo:network-device:down"
