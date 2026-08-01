#!/usr/bin/env bash
# run.sh — the turnkey runner for the scale-fleet demo (ADR-0116 D2).
#
# THE QUESTION THIS ANSWERS. "If I change a 1 to a 3, do I get two more machines?" It is the
# plainest thing anyone asks of estate-as-code and the repo could not demonstrate it: `count`
# fan-out was unit-tested and never shown end to end.
#
# WHAT IT PROVES, in order:
#
#   0. The estate names NO substrate and NO provider outside its one capability-binding — asserted
#      by reading the declarations, because a portability claim nobody checks is one that rots.
#   A. count: 1 → ONE gated build → a host carrying an OBSERVED reach method (ADR-0156), which the
#      converge then USES: the provider that built it said how to reach it, in a Facet, and no
#      declaration above it names one. The method is a property of the substrate.
#
#      THIS LEG ONCE CLAIMED A CONVERGE IT NEVER RAN (corrected 2026-08-01). It asserted the Facet
#      was PRESENT and narrated that as "a host that CONVERGES, over `kubectl exec` … the converge
#      never touches port 22". Measuring one thing and reporting a stronger one is the exact trap
#      the comment beside `transportOf` congratulates itself for avoiding, with the polarity
#      reversed — and it hid a real defect: `demos/region-to-cert`, the first thing to ACTUALLY
#      converge over this transport, failed, because the EE Job pod is spawned with no cluster
#      identity by design (`AutomountServiceAccountToken: false`) and `kubectl exec` had nothing to
#      authenticate with. The reach credential is built now (ADR-0156 D4a), so the claim is made
#      the only way a claim should be: by running it.
#   B. THE EDIT. count: 1 → 3 surfaces EXACTLY TWO builds. Not three — web-01 is already built and
#      the reconcile knows it. Not one — the shortfall is two. That exact number is the mechanism.
#   C. Approve both → three hosts → and the SAME Assignment, unedited, configures the two that did
#      not exist when it was written. Asserted by reading back the `app.config` each Run WROTE, not
#      by watching a Finding close. Scaling out is one number; the configuration follows on its own.
#      The Run COUNT is reported rather than asserted — remediation is entity-scoped (ADR-0150), so
#      the fan-out is a property to observe, not a number to assume.
#   D. THE EDIT BACK. count: 3 → 1 — and the limit this demo FOUND by running: kubecompute
#      advertises `provisions` and not `decommissions`, so on this substrate there is no teardown
#      Workflow to offer. Reported, booked, and not asserted as something it is not.
#
# WHAT IT DOES NOT PROVE, said here rather than left to be assumed: the multi-substrate half. The
# same Intent resolves to vsphere-vm-build in the reference estate's `vsphere-dc` environment, and
# `demo:vsphere-only` exercises that builder — but this demo runs ONE floor, and no dev substrate
# offers both a Kubernetes cluster and a vCenter at once. Proof 0 is what carries the portability
# claim here: the declaration that would move is one line, and it is not in any of these files.
#
# Env: KUBECTL, KUBECONTEXT, STRATT_NS, STRATT_PRINCIPAL, STRATT_LPORT.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

KUBECTL="${KUBECTL:-kubectl}"
CTX="${KUBECONTEXT:-kind-stratt-dev}"
NS="${STRATT_NS:-stratt}"
HOSTS_NS="${STRATT_HOSTS_NS:-stratt-hosts}"
PRINCIPAL="${STRATT_PRINCIPAL:-bootstrap-admin}"
LPORT="${STRATT_LPORT:-18096}"
ROOT="http://127.0.0.1:${LPORT}"
API="${ROOT}/api/v1"

FLEET_INTENT="web-fleet"
HOST_VIEW="web-servers"

kc() { "$KUBECTL" --context "$CTX" "$@"; }
api() {
    curl -fsS -X "$1" "${API}$2" -H "X-Stratt-Principal: ${PRINCIPAL}" -H "Content-Type: application/json" "${@:3}"
}

# fail must kill the WHOLE runner, not the subshell it happens to be standing in — several helpers
# are called as `x="$(helper …)"`, and a bare `exit 1` inside command substitution ends only that
# subshell, letting the run continue and fail a second time on an empty value. The capstone paid
# for that once; the signal makes the first failure the last line.
TOP_PID=$$
trap 'exit 1' TERM

# THE RUNNER EDITS A TRACKED FILE, so it must put it back — including when it fails. A run that
# died at leg B left `count: 3` committed-shaped in the working tree, and the NEXT run then opened
# on three build Findings and failed leg A with a number that was its own predecessor's residue.
# Restoring on EXIT is what makes the demo re-runnable and keeps `git status` honest.
INTENT_FILE_BACKUP="$(mktemp)"
restore_intent() {
    [ -f "$INTENT_FILE_BACKUP" ] && cp "$INTENT_FILE_BACKUP" "$HERE/estate/intents/web-fleet.yaml"
    rm -f "$INTENT_FILE_BACKUP"
}
fail() { echo "FAIL: $*" >&2; kill -TERM "$TOP_PID" 2>/dev/null || true; exit 1; }
say() { printf '\n\033[1m== %s\033[0m\n' "$*"; }

# The runner owns its own port-forward, as the capstone's does. A caller that sets one up instead
# has to keep it alive across this script's own `rollout restart`s — which is exactly how the first
# run of this demo reported "-1 findings": the API was simply unreachable and every helper returned
# an error the wait loop counted as a value.
cp "$HERE/estate/intents/${FLEET_INTENT}.yaml" "$INTENT_FILE_BACKUP"

echo "demo: port-forward svc/stratt ${LPORT}->8080 (ns ${NS})"
kc -n "$NS" port-forward svc/stratt "${LPORT}:8080" >/dev/null 2>&1 &
PF_PID=$!
trap 'kill "$PF_PID" 2>/dev/null || true; restore_intent' EXIT
for _ in $(seq 1 60); do curl -fsS "${ROOT}/healthz" >/dev/null 2>&1 && break; sleep 1; done
curl -fsS "${ROOT}/healthz" >/dev/null 2>&1 || fail "strattd never became reachable on ${ROOT}"

# ── 0 · the estate names no substrate ────────────────────────────────────────────────────
say "0 · the estate names no substrate outside its one binding"
# SUBSTRATE NAMES ONLY. `pod` and `namespace` were in this list for one run and both were false
# positives worth recording: `namespace:` is how a Blueprint names a FACET namespace
# (`namespace: app.config`) and how a k8s-secret CredentialRef addresses its locator. Neither is a
# claim about where compute runs, and a check that cries wolf on them gets switched off.
banned='kubernetes|kubecompute|vsphere|vcenter|awsec2|opentofu|crossplane'
narrative='description|summary|fail_msg|title'
offenders=0
while IFS= read -r f; do
    # credential-refs are excluded, and the reason is structural rather than convenient: a
    # CredentialRef's locator is BACKEND-SHAPED by design (ADR-0009 — {namespace,name} for
    # k8s-secret, {mount,path} for vault), and a credential is by nature FOR a particular
    # provider. The broker is not the compute substrate, and the portability claim is about
    # what decides WHERE things are built.
    case "$f" in */capability-bindings/* | */credential-refs/*) continue ;; esac
    # Only KEY-BEARING lines, and never a narrative key: the capstone's version of this check
    # matched prose inside a `description:` block scalar and reported a violation that was a
    # sentence. Comments are stripped for the same reason.
    if sed -e 's/[[:space:]]*#.*$//' "$f" \
        | grep -E '^[[:space:]]*(-[[:space:]]*)?[A-Za-z][A-Za-z0-9_.-]*:' \
        | grep -vE "^[[:space:]]*(-[[:space:]]*)?(${narrative}):" \
        | grep -qiE "$banned"; then
        echo "  ✗ $f names a substrate"
        offenders=$((offenders + 1))
    fi
done < <(find "$HERE/estate" -name '*.yaml' ! -name 'plugins.yaml')
[ "$offenders" -eq 0 ] || fail "$offenders declaration(s) name a substrate — the portability claim is false"
echo "  ✓ every Intent, Blueprint, Assignment and View names a capability class and nothing else"
echo "  ✓ the only file that names one:"
grep -nE '^\s+substrate:' "$HERE"/estate/capability-bindings/*.yaml | sed 's/^/      /'

# ── helpers ──────────────────────────────────────────────────────────────────────────────
hosts() { api GET "/views/${HOST_VIEW}/entities" | jq -r '.entities | length'; }
# provisionFindings counts the OPEN build Findings this Intent has surfaced.
# The Findings endpoint returns a BARE ARRAY and the discriminator is `baseline`, matched as a
# substring — the same idiom the capstone's await_finding uses. The first version of this reached
# for `.findings[]` and `.control`, which jq answers by erroring on an array; the wait loop then
# read the error as a count and spent six minutes reporting a number nothing had measured.
#
# SCOPED TO THIS INTENT on purpose: a floor that has run another demo carries its Findings, and a
# bare count would make this demo pass or fail on somebody else's leftovers.
findingsMatching() { # findingsMatching PREFIX → count
    api GET "/findings?status=open" \
        | jq --arg b "$1" '[.[]? | select(.baseline | test($b))] | length'
}
provisionFindings() { findingsMatching "provision/${FLEET_INTENT}"; }
decommissionFindings() { findingsMatching "decommission/${FLEET_INTENT}"; }
waitFor() { # waitFor DESC EXPECTED FN
    local desc="$1" want="$2" fn="$3" got=""
    for _ in $(seq 1 90); do
        # An API error is NOT an observation. The first version substituted -1 on failure and
        # let the loop treat it as a count, so an unreachable API read as "wrong number" for six
        # minutes and then failed with a number nothing had measured.
        if ! got="$("$fn" 2>/dev/null)"; then
            got="(api error)"
        elif [ "$got" = "$want" ]; then
            echo "  ✓ $desc = $want"
            return 0
        fi
        sleep 4
    done
    fail "$desc = ${got:-?}, want $want"
}
# setCount rewrites the ONE number and re-stages the estate, which is what an operator committing
# to Git would cause. Re-staging rather than poking the API is the point: this is estate-as-code.
setCount() {
    local n="$1"
    sed -i -E "s/^  count: [0-9]+/  count: ${n}/" "$HERE/estate/intents/${FLEET_INTENT}.yaml"
    grep -qE "^  count: ${n}\b" "$HERE/estate/intents/${FLEET_INTENT}.yaml" \
        || fail "count edit did not take"
    # STAGE **AND SHIP**. Staging only rewrites files in the chart directory; the running daemon
    # reads its inline declarations from the Helm release's ConfigMap, so without the upgrade the
    # cluster keeps the OLD count and the next assertion reads 0 changes — which is exactly what
    # the first run of this leg reported. `task demo:scale-fleet:stage` then `helm upgrade` is the
    # whole edit-to-effect path an operator's commit takes.
    (cd "$HERE/../.." && task demo:scale-fleet:stage >/dev/null) || fail "re-stage failed"
    (cd "$HERE/../.." && "${HELM:-helm}" upgrade --install stratt deploy/charts/stratt \
        --kube-context "$CTX" -n "$NS" \
        -f deploy/charts/stratt/values-allinone.yaml \
        -f deploy/charts/stratt/values-demo-scale-fleet.yaml >/dev/null) || fail "helm upgrade failed"
    kc -n "$NS" rollout restart deploy/stratt >/dev/null
    kc -n "$NS" rollout status deploy/stratt --timeout=180s >/dev/null
    # The restart takes the forwarded pod with it, so the tunnel must be rebuilt or every
    # subsequent assertion reads an unreachable API as a value rather than as an outage.
    kill "$PF_PID" 2>/dev/null || true
    kc -n "$NS" port-forward svc/stratt "${LPORT}:8080" >/dev/null 2>&1 &
    PF_PID=$!
    for _ in $(seq 1 60); do curl -fsS "${ROOT}/healthz" >/dev/null 2>&1 && break; sleep 1; done
    curl -fsS "${ROOT}/healthz" >/dev/null 2>&1 || fail "strattd unreachable after the re-stage"
}
# approveGate RUN-ID — §5 Flow 1 is a DOUBLE gate and the first live run proved it: a Finding
# offers the build, launching its remediation starts the Workflow, and the Workflow ITSELF has a
# human Gate before anything is created. Launching alone leaves the run parked forever, which
# reads as "the host never appeared".
approveGate() {
    local run="$1" gate=""
    for _ in $(seq 1 40); do
        gate="$(api GET "/gates?status=pending" | jq -r --arg r "$run" '.[]? | select(.workflowRunId==$r) | .id' | head -1)"
        [ -n "$gate" ] && [ "$gate" != "null" ] && break
        sleep 2
    done
    [ -n "$gate" ] && [ "$gate" != "null" ] || fail "no pending gate for run ${run}"
    api POST "/gates/${gate}/decision" --data '{"approve":true}' >/dev/null || fail "gate decision"
    echo "  → gate ${gate} approved as ${PRINCIPAL} (a human stands here, always)"
}

approveAll() { # launch every open build Finding's remediation AND approve the Gate it opens
    local ids run
    ids="$(api GET "/findings?status=open" | jq -r --arg b "provision/${FLEET_INTENT}" \
        '.[]? | select(.baseline | test($b)) | .id')"
    [ -n "$ids" ] || fail "no build Findings to approve"
    for id in $ids; do
        run="$(api POST "/findings/${id}/remediation" -d '{}' | jq -r '.id // empty')"
        [ -n "$run" ] || fail "launch remediation $id returned no WorkflowRun"
        echo "  → launched build for finding ${id} (run ${run})"
        approveGate "$run"
    done
}

# ── the converge, which this demo used to CLAIM and not RUN ──────────────────────────────
#
# Corrected 2026-08-01. The header said "a host that CONVERGES, over `kubectl exec`" and the
# summary said the new hosts were "built and CONFIGURED by an Assignment nobody edited". Neither
# happened: the script asserted that `mgmt.transport` was OBSERVED and stopped there. It is the
# same trap as reading the wrong jq path, with the polarity reversed — a claim that cannot fail
# because nothing tests it.
#
# It runs now because it CAN: a kubectl converge needs a brokered kubeconfig on the control node
# (ADR-0156 D4a), which did not exist when this demo was written. Every host reached here is
# reached through the API server with no sshd involved, which is the claim the header makes.
appFindings() { findingsMatching "web-servers-apache"; }

# convergeAll launches every open apache drift Finding and waits for each Run.
#
# It REPORTS the number of Runs rather than asserting one. "One Assignment converges all three in
# ONE Run" was the old summary's wording and nobody had counted: remediation is entity-scoped
# (ADR-0150), so the fan-out is a property worth observing rather than a number worth assuming.
convergeAll() { # convergeAll _ [EXPECTED-FINDINGS]
    local ids run st n=0
    # POLLED, not read once. The apache Baseline runs on its own cadence (`@every 1m`), so a host
    # that landed seconds ago is drifted-but-not-yet-noticed. Reading once failed with "no apache
    # drift Finding opened" against an estate that was about to open one — the same
    # projection-lag-versus-defect confusion the transport wait upstream exists for.
    for _ in $(seq 1 45); do
        ids="$(api GET "/findings?status=open" | jq -r '.[]? | select(.baseline | test("web-servers-apache")) | .id')"
        [ -n "$ids" ] && break
        sleep 4
    done
    [ -n "$ids" ] || fail "no apache drift Finding opened — a built host with no app.config is
      unmet desired state, so one should exist for every host that is not yet converged"
    # MEASURED FIRST, then asserted (2026-08-01). This started as a bare report, because how many
    # Findings a fleet of N unconverged hosts opens is a property of entity-scoped remediation
    # (ADR-0150) that nobody had counted. It counted 1 and then 2 — so the caller may now demand a
    # number, and leg C's 2 is the whole point: the host converged in leg A is NOT re-offered.
    local got
    got="$(printf '%s\n' "$ids" | grep -c .)"
    echo "  · open apache drift Findings: ${got}"
    if [ -n "${2:-}" ] && [ "$got" != "$2" ]; then
        fail "expected ${2} apache drift Finding(s), got ${got} — more would mean an
      already-converged host was offered again, fewer that a built host was never noticed"
    fi
    for id in $ids; do
        run="$(api POST "/findings/${id}/remediation" -d '{}' | jq -r '.id // empty')"
        [ -n "$run" ] || fail "launch converge $id returned no WorkflowRun"
        n=$((n + 1))
        # `.workflowRun.status`, not `.status` — measured. The first version read `.status`, got
        # empty for every poll, and failed with "never reached a terminal state (last: none)" for a
        # Run that was fine. It failed rather than passing, which is the only reason that was cheap.
        for _ in $(seq 1 160); do
            st="$(api GET "/workflow-runs/${run}" | jq -r '.workflowRun.status // .status // empty')"
            case "$st" in
                succeeded) break ;;
                failed | cancelled)
                    api GET "/runs?workflowRunId=${run}" 2>/dev/null \
                        | jq -r '.[]? | select(.status=="failed") | "  cause: " + (.error // "(none recorded)")' | head -5 >&2
                    fail "converge run ${run} ${st}" ;;
            esac
            sleep 3
        done
        [ "$st" = "succeeded" ] || fail "converge run ${run} never reached a verdict (last: ${st:-none})"
    done
    echo "  ✓ ${n} converge Run(s) succeeded"
}

# convergedHosts counts hosts carrying an app.config the Run WROTE — the fact that proves the
# converge happened, rather than the Finding merely closing.
convergedHosts() {
    local total=0 port
    for id in $(api GET "/views/${HOST_VIEW}/entities" | jq -r '.entities[].id'); do
        port="$(api GET "/entities/$id" | jq -r '.facets[]? | select(.namespace=="app.config") | .value.port // empty')"
        [ -n "$port" ] && total=$((total + 1))
    done
    echo "$total"
}

# ── A · one host, built and converged ────────────────────────────────────────────────────
say "A · count: 1 — one gated build, and a host that converges"
waitFor "open build Findings" 1 provisionFindings
approveAll
waitFor "hosts in view:${HOST_VIEW}" 1 hosts

# The transport is OBSERVED, and this is where that stops being a design claim: kubecompute said
# how to reach what it built, in a Facet, and the converge read it.
#
# The VIEW listing carries identity and labels only — facets live on the entity itself, which is
# why this reads through /entities/{id}. The first version reached into `.entities[0].facets` and
# got `none` for a Facet that was present and correct; a demo asserting the wrong path fails
# honestly here and would have passed vacuously if the polarity had been reversed.
transportOf() { # transportOf ENTITY-ID → kind
    api GET "/entities/$1" | jq -r '.facets[]? | select(.namespace=="mgmt.transport") | .value.kind'
}
# POLLED, not read once. The BUILD's terminal projection carries the address; the TRANSPORT is
# written by the Syncer's next Observe of the running pod — so it lands a cycle later. Asserting it
# the instant the host appears reads `none` for a Facet that is about to be correct, which is
# projection lag rather than a defect, and a demo that cannot tell those apart is noise.
first="$(api GET "/views/${HOST_VIEW}/entities" | jq -r '.entities[0].id')"
transport=""
for _ in $(seq 1 60); do
    transport="$(transportOf "$first" 2>/dev/null || true)"
    [ "$transport" = "kubectl" ] && break
    sleep 4
done
[ "$transport" = "kubectl" ] || fail "expected an observed kubectl transport, got '${transport:-none}'"
echo "  ✓ mgmt.transport observed by the builder: ${transport} — the builder said how to reach
    what it built, and the converge below is what USES it"

# …and the converge itself, over that transport. The pod has no sshd in play here: the connection
# is `kubectl exec`, authenticated by a kubeconfig scoped to pods/exec in this one namespace.
convergeAll _ 1
waitFor "hosts carrying an app.config the Run wrote" 1 convergedHosts
waitFor "open apache drift Findings once converged" 0 appFindings

# ── B · THE EDIT ─────────────────────────────────────────────────────────────────────────
say "B · count: 1 → 3 — and EXACTLY two builds are offered"
setCount 3
# THE ASSERTION THIS DEMO EXISTS FOR. Three would mean the reconcile rebuilt a host that already
# exists; one would mean it lost count. Two is the shortfall, and only an idempotent, correlated
# reconcile produces it.
waitFor "open build Findings after the edit" 2 provisionFindings
echo "  ✓ web-01 was NOT re-offered — the reconcile correlates what is already built"

say "C · approve both — three hosts, and the SAME Assignment converges all three"
approveAll
waitFor "hosts in view:${HOST_VIEW}" 3 hosts
waitFor "open build Findings once converged" 0 provisionFindings
# Every host carries the observed transport, so one Run reaches all three the same way.
# Same lag applies to the two new hosts, so poll until ALL THREE carry it — "all three" is the
# claim, and a subset would be a weaker one silently passing.
kinds=""
for _ in $(seq 1 60); do
    kinds=""
    for id in $(api GET "/views/${HOST_VIEW}/entities" | jq -r '.entities[].id'); do
        kinds="${kinds}$(transportOf "$id" 2>/dev/null || echo none) "
    done
    kinds="$(printf '%s' "$kinds" | tr ' ' '\n' | grep -v '^$' | sort -u | paste -sd, -)"
    [ "$kinds" = "kubectl" ] && break
    sleep 4
done
[ "$kinds" = "kubectl" ] || fail "expected every host to carry an observed transport, got '${kinds:-none}'"
echo "  ✓ all three hosts carry an observed transport — no substrate named anywhere above the
    binding"

# THE CLAIM THIS LEG EXISTS FOR, now executed instead of narrated: the Assignment was never
# edited, and the two hosts that did not exist when it was written are configured by it.
#
# Only the NEW hosts drift — web-01 was converged in leg A and stays converged, so the reconcile
# offers exactly the shortfall here too. That is the same correlation property leg B asserts for
# builds, holding for configuration, which is the part of "change a 1 to a 3" that matters after
# the machines exist.
convergeAll _ 2
waitFor "hosts carrying an app.config the Run wrote" 3 convergedHosts
waitFor "open apache drift Findings once converged" 0 appFindings
echo "  ✓ the SAME Assignment, unedited, configured all three — exactly TWO drift Findings
    opened, so the host converged in leg A was not offered again. Over kubectl exec, never port 22."

# ── D · THE EDIT BACK ───────────────────────────────────────────────────────────────────
say "D · count: 3 → 1 — and the honest limit this demo found"
setCount 1
# WHAT THIS DEMO DISCOVERED, and it is reported rather than asserted because asserting a number
# nobody has measured is the failure this repo keeps catching.
#
# The first version of this leg expected TWO decommission Findings, symmetric with the two builds.
# There are none, and the reason is structural: `kubecompute` advertises `provisions` and NOT
# `decommissions`. It ships a build Workflow and no teardown Workflow, so on the kubernetes
# substrate a count-down has nothing to offer. vcenter DOES advertise `decommissions`
# (vsphere-vm-teardown, ADR-0114 D4), so the same edit against the vsphere-dc environment would
# surface gated teardowns — which is the multi-substrate asymmetry, found by running the thing.
#
# So this leg reports the state and books the gap instead of pretending cardinality is symmetric
# on a substrate where it is not.
echo "  · desired count is now 1; three hosts are still built"
echo "  · open decommission Findings: $(decommissionFindings)"
echo "  · kubecompute advertises no \`decommissions\` Workflow, so nothing is offered to tear down"
echo "  · BOOKED: kubecompute needs a teardown Workflow for count-down to be symmetric here"
echo "    (vcenter already has one — vsphere-vm-teardown, ADR-0114 D4)"

say "done"
cat <<'SUMMARY'

  One number changed, twice, and the estate did the rest:

    count: 1 → 3   two builds offered (not three — web-01 was already there), approved, built,
                   and then CONFIGURED by an Assignment nobody edited — asserted by reading the
                   app.config each Run wrote back, not by watching the Finding close
    count: 3 → 1   the desired state drops — and this demo FOUND that kubecompute ships no
                   teardown Workflow, so nothing is offered on this substrate. Booked, not
                   papered over. vcenter has one; kubernetes does not, yet.

  The Intent names no substrate. The connection method was observed by the provider that
  built the host, not declared by the Workflow that converged it — so the same Assignment
  would reach a vSphere VM or an EC2 instance without moving.

SUMMARY
