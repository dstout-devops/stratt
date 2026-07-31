#!/usr/bin/env bash
# run.sh — the turnkey runner for the region-to-cert capstone (ADR-0116 D2).
#
# It drives the WHOLE estate-as-code chain on a Stratt floor whose desired state IS this demo's
# estate, and asserts each leg rather than narrating it. `task demo:region-to-cert:run` stands the
# floor up (kind + strattd + five plugin pods + two EE images + the EC2 API + the IPAM + the object
# store + the CLM) before invoking this script. Needs curl + jq + kubectl.
#
# WHAT IT PROVES, in order:
#
#   0. The estate names NO substrate and NO provider outside its one capability-binding. Asserted by
#      reading the declarations, because a portability claim nobody checks is one that rots.
#   A. LEG 1 — a network, on the aws substrate. A declared size+pool becomes a CIDR an ALLOCATOR
#      chose, applied by a real `tofu apply`, projected back, and the build Finding closes. The
#      allocated range appears nowhere in the estate — which is the only way to tell an allocation
#      from a decoration.
#   B. LEG 2/3/4 — a host, an app and a certificate, on the kubernetes substrate. A fleet of one
#      builds; the provider reports the DNS name it CAUSED; the same converge recipe that serves a
#      hand-declared host installs Apache on it; a key is born on that host and never moves; a REAL
#      CA signs a certificate whose subject was derived from the host's own observed address; and
#      both drift Findings close because the estate actually converged.
#
# WHY A AND B DO NOT JOIN: no dev substrate has both bootable machines and networks, and the
# placement resolver correctly refuses to put a pod in an AWS subnet. The demo says so rather than
# staging a join that does not exist — see README "why this is two proofs and not one chain".
#
# Env: KUBECTL, KUBECONTEXT, STRATT_NS, STRATT_PRINCIPAL, STRATT_LPORT.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

KUBECTL="${KUBECTL:-kubectl}"
CTX="${KUBECONTEXT:-kind-stratt-dev}"
NS="${STRATT_NS:-stratt}"
HOSTS_NS="${STRATT_HOSTS_NS:-stratt-hosts}"
PRINCIPAL="${STRATT_PRINCIPAL:-bootstrap-admin}"
LPORT="${STRATT_LPORT:-18094}"
ROOT="http://127.0.0.1:${LPORT}"
API="${ROOT}/api/v1"

SUBNET_INTENT="app-subnet"
DMZ_INTENT="dmz-subnet"
FLEET_INTENT="web-fleet"
SUBNET_VIEW="built-subnets"
HOST_VIEW="web-servers"
CA_CN="Stratt Dev Root CA"
POOL_PREFIX="10.30."

kc() { "$KUBECTL" --context "$CTX" "$@"; }
api() { # api METHOD PATH — always as the bootstrap-admin Principal (dev header)
    curl -fsS -X "$1" "${API}$2" -H "X-Stratt-Principal: ${PRINCIPAL}" -H "Content-Type: application/json" "${@:3}"
}
# fail must kill the WHOLE runner, not the subshell it happens to be standing in.
#
# Several helpers return a value on stdout, so they are called as `x="$(helper …)"` — and a bare
# `exit 1` inside command substitution ends only that subshell. Measured: a failed remediation
# preview printed its FAIL, the substitution returned empty, and the run carried on to fail a
# SECOND time on the empty value ("no pending gate for run ") — burying the real cause under a
# consequence. Signalling the top-level PID makes the first failure the last line.
TOP_PID=$$
trap 'exit 1' TERM
fail() {
    echo "FAIL: $*" >&2
    kill -TERM "$TOP_PID" 2>/dev/null || true
    exit 1
}

# ── Surface the declared fidelity up front (D3: never present simulated as real) ─────────────────
fidelity="$(grep -E '^fidelity:' "${HERE}/demo.yaml" | head -1 | sed 's/^fidelity:[[:space:]]*//')"
echo "┌───────────────────────────────────────────────────────────────────────────"
echo "│ demo: region-to-cert (the capstone)     fidelity: ${fidelity}"
echo "│ kubernetes leg: real end to end. network leg: build-real (real tofu, real EC2 API,"
echo "│ real IPAM). No host is placed in the built subnet — two proofs, not one chain."
echo "└───────────────────────────────────────────────────────────────────────────"

# ══ PROOF 0 ══ The estate names no substrate and no provider ═════════════════════════════════════
#
# Everything an operator writes — Intents, Blueprints, Assignments, Views, the composed Workflow —
# must name capability CLASSES only (§1.5, ADR-0110). Exactly ONE file is allowed to name a
# substrate or a provider: capability-bindings/, which is where that decision belongs and where
# changing one word migrates the topology (ADR-0151 D2).
#
# Asserted here rather than claimed in a README, because this is the property the whole
# plugin-port architecture exists to deliver and it is trivially lost by one convenient edit.
#
# IT LOOKS AT DECLARED FIELDS, NOT AT PROSE, and the distinction is not pedantry — the first version
# of this check stripped comments and grepped whatever was left, and it failed on its first live run
# against `cert-issue.yaml`. What it caught was a `description:` block scalar explaining that the
# Workflow's `role` input names a CLM ROLE "rather than an OpenBao path, so a step-ca implementation
# of the same capability serves the same Intent unchanged". That text is the portability claim being
# MADE, and the check flagged it as the claim being broken.
#
# So: keep only lines that declare a key, and drop the ones whose key is narrative. A declaration
# that SELECTS something says so in a field.
echo "demo: assert the scenario declarations name no substrate and no provider"
banned='kubernetes|kubecompute|opentofu|openbao|netbox|awss3|seaweedfs|vsphere|vcenter|crossplane|floci'
narrative='description|summary|fail_msg|title'
offenders=""
for f in "${HERE}"/estate/intents/*.yaml "${HERE}"/estate/blueprints/*.yaml \
    "${HERE}"/estate/assignments/*.yaml "${HERE}"/estate/views/*.yaml \
    "${HERE}"/estate/workflows/*.yaml; do
    if sed -e 's/[[:space:]]*#.*$//' "$f" |
        grep -E '^[[:space:]]*(-[[:space:]]*)?[A-Za-z][A-Za-z0-9_.-]*:' |
        grep -vE "^[[:space:]]*(-[[:space:]]*)?(${narrative}):" |
        grep -qiE "$banned"; then
        offenders="${offenders} $(basename "$(dirname "$f")")/$(basename "$f")"
    fi
done
[ -z "$offenders" ] || fail "these declarations name a substrate or a provider:${offenders}
      Only capability-bindings/ may. That is the ADR-0151 D2 property — one line selects the
      landscape and nothing above it moves — and it is what makes this estate portable at all."
echo "  clean: the only file naming a landscape is capability-bindings/region-to-cert.yaml"

echo "demo: port-forward svc/stratt ${LPORT}->8080 (ns ${NS})"
kc -n "$NS" port-forward svc/stratt "${LPORT}:8080" >/dev/null 2>&1 &
PF_PID=$!
trap 'kill "$PF_PID" 2>/dev/null || true' EXIT
for _ in $(seq 1 60); do curl -fsS "${ROOT}/healthz" >/dev/null 2>&1 && break; sleep 1; done
curl -fsS "${ROOT}/healthz" >/dev/null 2>&1 || fail "strattd never became reachable on ${ROOT}"

# ── shared helpers ───────────────────────────────────────────────────────────────────────────────

# await_finding BASELINE-SUBSTRING [TRIES] → echoes the Finding id
await_finding() {
    local want="$1" tries="${2:-60}" id=""
    for _ in $(seq 1 "$tries"); do
        id=$(api GET "/findings?status=open" 2>/dev/null |
            jq -r --arg b "$want" '.[]? | select(.baseline | test($b)) | .id' | head -1)
        [ -n "$id" ] && [ "$id" != "null" ] && { echo "$id"; return 0; }
        sleep 3
    done
    return 1
}

# approve_gate RUN-ID — waits for the pending gate on that WorkflowRun and approves it.
# §5 Flow 1: a build is never auto-launched and never auto-approved. The runner stands in for the
# human, through the REAL door — POST /gates/{id}/decision → authz check → Temporal signal.
approve_gate() {
    local run="$1" gate=""
    for _ in $(seq 1 40); do
        gate=$(api GET "/gates?status=pending" | jq -r --arg r "$run" '.[]? | select(.workflowRunId==$r) | .id' | head -1)
        [ -n "$gate" ] && [ "$gate" != "null" ] && break
        sleep 2
    done
    [ -n "$gate" ] && [ "$gate" != "null" ] || fail "no pending gate for run ${run}"
    # >&2 throughout these helpers: inside build_subnet their stdout would be captured as the
    # function's RETURN VALUE, and a progress line would silently become the allocated CIDR.
    echo "  approving gate ${gate} as ${PRINCIPAL} (platform-admins)" >&2
    api POST "/gates/${gate}/decision" --data '{"approve":true}' >/dev/null
}

# await_run RUN-ID LABEL [TRIES]
await_run() {
    local run="$1" what="$2" tries="${3:-120}" st=""
    for _ in $(seq 1 "$tries"); do
        st=$(api GET "/workflow-runs/${run}" | jq -r '.workflowRun.status // .status // empty')
        case "$st" in
            succeeded) echo "  ${what}: WorkflowRun succeeded" >&2; return 0 ;;
            failed | cancelled)
                echo "  ${what}: WorkflowRun ${st}" >&2
                api GET "/runs?workflowRunId=${run}" 2>/dev/null |
                    jq -r '.[]? | select(.status=="failed") | "  cause: " + (.error // "(none recorded)")' | head -5 >&2
                fail "${what} did not converge" ;;
        esac
        sleep 3
    done
    fail "${what}: WorkflowRun never reached a verdict (last=${st:-none})"
}

# await_resolved FINDING-ID LABEL
await_resolved() {
    local id="$1" what="$2" st=""
    for _ in $(seq 1 40); do
        st=$(api GET "/findings/${id}" | jq -r '.status')
        [ "$st" = "resolved" ] && { echo "  ${what}: Finding resolved — expectation and observation agree" >&2; return 0; }
        sleep 3
    done
    api GET "/findings/${id}" | jq -r '"  diff: " + (.diff | tostring)' >&2
    fail "${what}: Finding ${id} is '${st}', not resolved"
}

# remediate FINDING-ID EXPECTED-WORKFLOW → echoes the WorkflowRun id
# §1.8: the door RENDERS what it would launch before launching it. Asserted, because a door that
# acts without showing its hand is the thing ADR-0118 D3 refused to ship.
remediate() {
    local id="$1" want="$2" prev="" wf run
    # RETRY the preview. A Finding becomes visible on /findings the moment it is written, while its
    # remediation is resolved from the compiled route — so on a floor that has just booted the two
    # are briefly out of step and the preview 404s. Measured on the first run against a fresh helm
    # install; the same script had passed against a floor that had been up for a while, which is
    # exactly the kind of difference that makes a demo "flaky" rather than wrong.
    for _ in $(seq 1 30); do
        prev="$(api GET "/findings/${id}/remediation" 2>/dev/null)" && [ -n "$prev" ] && break
        prev=""
        sleep 3
    done
    [ -n "$prev" ] || fail "finding ${id} never offered a remediation — it is open, and nothing
      resolved a Workflow to close it. GET /findings/${id}/remediation returned nothing."
    wf="$(jq -r '.workflow' <<<"$prev")"
    [ "$wf" = "$want" ] || fail "the remediation preview names workflow '${wf}', want ${want}"
    echo "  preview: workflow=${wf} params=$(jq -c '.params' <<<"$prev")" >&2
    run=$(api POST "/findings/${id}/remediation" | jq -r '.id')
    [ -n "$run" ] && [ "$run" != "null" ] || fail "no WorkflowRun id returned for finding ${id}"
    echo "$run"
}

# facet ENTITY-ID NAMESPACE → echoes the facet object
facet() { api GET "/entities/$1" | jq -c --arg n "$2" '.facets[]? | select(.namespace==$n)'; }

# ══ PROOF A ══ LEG 1: a network, on the aws substrate ═══════════════════════════════════════════
echo
echo "══ PROOF A — the network leg (substrate: aws) ══════════════════════════════════════════════"

# build_subnet INTENT-NAME → echoes the allocated CIDR. Drives one Intent/Subnet all the way:
# await the gated build Finding, preview it, approve, wait, then read the built Entity back and
# check the two labels that carry the whole result.
build_subnet() {
    local intent="$1" finding run id labels cidr singleton

    echo "demo: awaiting the GATED build Finding for Intent/Subnet ${intent}" >&2
    finding="$(await_finding "provision/${intent}" 60)" ||
        fail "no provisioning Finding ever opened for ${intent} — the Intent declares a subnet that
      nothing offered to build. Most likely the provisioning capability did not resolve: check
      GET /actuators for opentofu-network pending on statestore or ipam."
    echo "  Finding ${finding} — a build OFFERED, never auto-launched (§5 Flow 1)" >&2

    run="$(remediate "$finding" opentofu-subnet-build)"
    approve_gate "$run"
    await_run "$run" "${intent} build" 200

    # The built subnet, read back through a View like everything else. NOTE the response shape: the
    # Entity is nested under `.entity`, its Facets are a sibling — reading `.labels` off the top
    # level yields null and every assertion below then compares nothing to something.
    for _ in $(seq 1 30); do
        id="$(api GET "/views/${SUBNET_VIEW}/entities" |
            jq -r --arg s "$intent" '.entities[]? | select(.labels["stratt.intent/singleton"] // "" | test($s)) | .id' | head -1)"
        [ -n "$id" ] && break
        sleep 2
    done
    [ -n "$id" ] || fail "${intent}: the build succeeded and no subnet Entity landed in view:${SUBNET_VIEW}"

    labels="$(api GET "/entities/${id}" | jq -c '.entity.labels // {}')"
    cidr="$(jq -r '.["net.cidr"] // empty' <<<"$labels")"
    singleton="$(jq -r '.["stratt.intent/singleton"] // empty' <<<"$labels")"
    echo "  ${intent}: subnet ${id}  net.cidr=${cidr:-<absent>}  singleton=${singleton:-<absent>}" >&2

    # The correlation label is what makes the NEXT reconcile see this Intent as built. Without it the
    # build goes green and the Finding is offered again forever (ADR-0120). The value is the
    # QUALIFIED singleton key, not the bare Intent name.
    [ "$singleton" = "Intent/Subnet/${intent}" ] ||
        fail "${intent}: built subnet carries stratt.intent/singleton='${singleton:-<absent>}',
      want 'Intent/Subnet/${intent}'. Without it the reconcile cannot tell this Intent is built."

    [ -n "$cidr" ] || fail "${intent}: the built subnet reports no net.cidr — the graph must hold what IS (§1.2)"
    case "$cidr" in
        "${POOL_PREFIX}"*/24) : ;;
        *) fail "${intent}: net.cidr='${cidr}' is not a /24 inside the declared pool ${POOL_PREFIX}0.0/16" ;;
    esac

    # If the ALLOCATED range could be found in the estate, the allocator would be decoration — the
    # estate would have hand-assigned the address and NetBox would merely have agreed.
    #
    # COMMENTS STRIPPED, for the second time in this script and for the same reason: the declarations
    # discuss the ranges they deliberately do not assign. This fired on dmz-subnet.yaml, whose comment
    # explains why 10.30.0.0/24 alone would not prove an allocation — the file arguing the point was
    # read as the point being violated.
    local hit=""
    for decl in "${HERE}"/estate/*/*.yaml; do
        if sed 's/[[:space:]]*#.*$//' "$decl" | grep -qF "$cidr"; then
            hit="${hit} ${decl}"
        fi
    done
    [ -z "$hit" ] || fail "${intent}: the allocated range ${cidr} is DECLARED in${hit} — then it was
      ASSIGNED, not allocated. \`size\` + \`pool\` is a request; which /24 you get is the
      allocator's answer (ADR-0111)."

    await_resolved "$finding" "${intent} build"
    echo "$cidr"
}

# TWO subnets, and the second one is what makes the allocator claim FALSIFIABLE. One Intent asking
# for a /24 out of 10.30.0.0/16 comes back 10.30.0.0/24 — exactly what a system that echoed the
# pool's base with the requested prefix would return, so at n=1 "an allocator ran" and "an allocator
# allocated" are indistinguishable. Two Intents asking for the SAME size from the SAME pool must
# come back DIFFERENT, which nothing but a real allocator holding real state can do.
cidr_app="$(build_subnet "$SUBNET_INTENT")"
cidr_dmz="$(build_subnet "$DMZ_INTENT")"

echo "demo: assert the two subnets got DIFFERENT ranges out of one pool"
[ "$cidr_app" != "$cidr_dmz" ] ||
    fail "both subnets were allocated ${cidr_app}. Two networks sharing one range is not an
      allocation — it is an echo of the pool, and it would collide in production with nothing
      anywhere to notice (ADR-0111: an allocator is the system of record, or it is decoration)."
echo "  ${SUBNET_INTENT}=${cidr_app}  ${DMZ_INTENT}=${cidr_dmz} — distinct, both inside ${POOL_PREFIX}0.0/16,"
echo "    neither written anywhere in the estate. NetBox allocated; the estate asked for a size."

# ══ PROOF B ══ LEG 2/3/4: a host, an app, a certificate — on the kubernetes substrate ════════════
echo
echo "══ PROOF B — build → converge → certificate (substrate: kubernetes) ════════════════════════"

echo "demo: awaiting the GATED build Finding for Intent/Compute ${FLEET_INTENT}"
fleet_finding="$(await_finding "provision/${FLEET_INTENT}" 60)" ||
    fail "no provisioning Finding ever opened for ${FLEET_INTENT}"
echo "  Finding ${fleet_finding}"

fleet_run="$(remediate "$fleet_finding" kubecompute-build)"
approve_gate "$fleet_run"
await_run "$fleet_run" "host build" 120

# ── The reach coordinate the provider CAUSED (ADR-0142 D4) ───────────────────────────────────────
# Not computed by the estate from a zone it guessed at, and not the pod IP — which changes on every
# restart and cannot be a certificate subject. The build creates the Service that MAKES this name
# resolve, and the Syncer observes it once the pod is Running.
echo "demo: awaiting the built host in View ${HOST_VIEW}, with the address its provider caused"
host_id=""; addr=""
for _ in $(seq 1 60); do
    host_id="$(api GET "/views/${HOST_VIEW}/entities" | jq -r '.entities[0].id // empty')"
    if [ -n "$host_id" ]; then
        addr="$(facet "$host_id" mgmt.address | jq -r '.value.address // empty')"
        [ -n "$addr" ] && break
    fi
    sleep 3
done
[ -n "$host_id" ] || fail "the build succeeded and no host landed in view:${HOST_VIEW}"
[ -n "$addr" ] || fail "host ${host_id} is built but has no mgmt.address — a builder that cannot say
      how to reach what it built has not finished the job, and the certificate leg has no subject."
echo "  host ${host_id}  mgmt.address=${addr}"
case "$addr" in
    *.svc.cluster.local) : ;;
    *) fail "mgmt.address='${addr}' is not the DNS name the provider caused" ;;
esac

host_pod="${addr%%.*}"

# ── LEG 3: the app ───────────────────────────────────────────────────────────────────────────────
echo "demo: awaiting the app drift Finding (a host with no app.config is desired state ABSENT)"
apache_finding="$(await_finding "web-servers-apache" 60)" ||
    fail "no drift Finding opened for the apache Assignment — the built host should be unmet the
      moment it lands in the View, because a missing Facet is unmet."
echo "  Finding ${apache_finding}"

apache_run="$(remediate "$apache_finding" apache-configure)"
await_run "$apache_run" "apache converge" 160

# OFF THE WIRE, not off a file: a task that ran proves a task ran; a served response proves the app
# is actually installed and listening on the port the Intent's Blueprint declared.
echo "demo: assert Apache serves on the built host, read off the wire"
kc -n "$HOSTS_NS" exec "$host_pod" -- wget -q -O- -T 10 http://127.0.0.1:8080/ >/dev/null ||
    fail "nothing served HTTP on 127.0.0.1:8080 on the host Stratt built"
echo "  the host Stratt built an hour of declarations ago is serving HTTP on :8080"

# The Run reported what it OBSERVED, under a bounded grant — and the provenance is the half that
# matters (§1.2): the value must be stamped as written by a RUN, not by a Syncer that invented it.
port="$(facet "$host_id" app.config | jq -r '.value.port // empty')"
writer="$(facet "$host_id" app.config | jq -r '.provenance.writerKind // empty')"
[ "$port" = "8080" ] || fail "app.config.port='${port:-<absent>}', want 8080"
[ "$writer" = "run" ] || fail "app.config was written by '${writer:-<none>}', expected a Run"
echo "  app.config.port=${port} writerKind=${writer} (facetWriteScope ∩ the Actuator's grant)"

await_resolved "$apache_finding" "apache converge"

# ── LEG 4: the certificate ───────────────────────────────────────────────────────────────────────
echo "demo: awaiting the certificate drift Finding (no certificate present is drift too)"
cert_finding="$(await_finding "web-servers-cert" 60)" ||
    fail "no Finding opened for the certificate Assignment"
echo "  Finding ${cert_finding}"

# The subject is DERIVED from this host's own observed address (ADR-0150 D1) — one Intent covers a
# fleet. Assert the preview carries the host's address, so we know it was resolved per-Entity and
# not authored per certificate.
cert_prev="$(api GET "/findings/${cert_finding}/remediation")"
prev_cn="$(jq -r '.params.commonName // empty' <<<"$cert_prev")"
[ "$prev_cn" = "$addr" ] ||
    fail "the remediation would issue for commonName='${prev_cn:-<absent>}', but this host's observed
      address is '${addr}'. The subject must be DERIVED from the host (ADR-0150 D1), never guessed."
echo "  the certificate's subject was derived from the host's own mgmt.address: ${prev_cn}"

cert_run="$(remediate "$cert_finding" cert-issue)"
await_run "$cert_run" "certificate issuance" 160

# ── BORN ON TARGET (§2.5, CERT-2) ────────────────────────────────────────────────────────────────
# Three Steps exist so that the KEY is made where it will be used, only the CSR travels, and the
# signature comes back. Assert the key is on the host at 0600 — the property the whole three-Step
# shape exists for, and the one a single "mint me a certificate" call cannot have.
echo "demo: assert the private key was born on the target and never moved"
keymode="$(kc -n "$HOSTS_NS" exec "$host_pod" -- sh -c "stat -c %a '/etc/stratt/pki/${addr}.key' 2>/dev/null" || true)"
[ "$keymode" = "600" ] ||
    fail "the private key at /etc/stratt/pki/${addr}.key has mode '${keymode:-<absent>}', want 600"
kc -n "$HOSTS_NS" exec "$host_pod" -- test -f "/etc/stratt/pki/${addr}.crt" ||
    fail "no certificate landed at /etc/stratt/pki/${addr}.crt"
echo "  key 0600 on the host, certificate beside it — the key never crossed the wire"

# ── SIGNED BY A REAL CA — the line the app-cert demo drew and could not cross ─────────────────────
# app-cert's certificate is minted by the play itself, self-signed, because a real CA is a Connector
# and not something a play should be. Here the signature comes from the `certissuer` capability, and
# cert.presented was PARSED OFF THE FILE that landed on the host (ADR-0150 D5) — so this asserts what
# the host actually holds, not what the CLM says it issued.
echo "demo: assert the certificate on the host was signed by a real CA, for the right subject"
presented="$(facet "$host_id" cert.presented)"
[ -n "$presented" ] || fail "the host reports no cert.presented facet after issuance"
iss="$(jq -r '.value.issuer // empty' <<<"$presented")"
cn="$(jq -r '.value.commonName // empty' <<<"$presented")"
notafter="$(jq -r '.value.notAfter // empty' <<<"$presented")"
echo "  cert.presented: CN=${cn}  issuer=${iss}  notAfter=${notafter}"
[ "$cn" = "$addr" ] || fail "the delivered certificate's CN is '${cn}', not the host's address '${addr}'"
[ "$iss" = "$CA_CN" ] ||
    fail "the delivered certificate's issuer is '${iss:-<absent>}', want '${CA_CN}'.
      A self-signed certificate (issuer == subject) would mean the play minted its own — which is
      exactly what this demo exists to stop doing (§1.2: one authority per fact)."
[ "$iss" != "$cn" ] || fail "issuer == subject: this certificate is self-signed"

await_resolved "$cert_finding" "certificate issuance"

echo
echo "demo: DONE — an estate that names no substrate produced two networks, a host, an app and a"
echo "      CA-signed certificate, through gated/previewed doors only (fidelity: ${fidelity})."
echo "  subnets: ${cidr_app} + ${cidr_dmz} (allocated by NetBox, built by tofu, in a real EC2 API)"
echo "  host:    ${addr} (built by Kubernetes, address CAUSED not computed)"
echo "  app:     Apache on :8080, observed back into the graph by the Run that installed it"
echo "  cert:    CN=${cn}, issued by ${iss}, key born on target at 0600, expires ${notafter}"
echo
echo "  Migrate the topology: edit estate/capability-bindings/region-to-cert.yaml — one word."
echo "  Watch the descent in the UI:  (cd ui && npm run dev)  → Intents → Findings → Runs"
echo "  Clean up:       task demo:region-to-cert:down   (full teardown: task dev:kind:down)"
