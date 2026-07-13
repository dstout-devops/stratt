#!/bin/sh
# Provision the dev CLM in the compose OpenBao: enable the PKI secrets engine,
# generate a root CA + an issuing role, and seed a few demo leaf certs for the
# cert-issuer Connector to project (Intent/Certificate GA, ADR-0030). This is
# the CA-side analogue of zitadel-bootstrap.sh — OpenBao dev mode is in-memory,
# so re-run after every `task dev:up`.
#
# Idempotent: the PKI mount, root, and role are created only if absent; demo
# leaf certs are issued only when no leaf certs exist yet (so a re-run against a
# live estate does not pile up duplicates).
#
# Demo certs (against renewBefore=360h/15d in the Certificate Blueprint):
#   web.stratt.test  ttl 720h (30d)  → healthy  (notAfter > now+15d) → no Finding
#   api.stratt.test  ttl 48h         → expiring (notAfter < now+15d) → Finding
#
# Usage: deploy/dev/openbao-bootstrap.sh  (or: task dev:openbao:bootstrap)
set -eu

base=${STRATT_CLM_ADDR:-http://localhost:8200}
token=${STRATT_CLM_TOKEN:-stratt-dev-root}
role=${STRATT_CLM_ROLE:-stratt-dev}

api() {
    method=$1
    path=$2
    body=${3:-}
    curl -fsS -X "$method" "$base$path" \
        -H "X-Vault-Token: $token" \
        -H "Content-Type: application/json" \
        ${body:+-d "$body"}
}

# Silent probe (no -f): returns body even on 4xx so callers can test presence.
probe() {
    curl -sS -o /dev/null -w '%{http_code}' -X "$1" "$base$2" -H "X-Vault-Token: $token"
}

echo "openbao-bootstrap: target $base"

# 1. PKI secrets engine at /pki (mounts are idempotent-by-404).
if [ "$(probe GET /v1/sys/mounts/pki/tune)" = "200" ]; then
    echo "exists  pki secrets engine"
else
    api POST /v1/sys/mounts/pki '{"type":"pki"}' >/dev/null
    echo "created pki secrets engine"
fi
api POST /v1/sys/mounts/pki/tune '{"max_lease_ttl":"87600h"}' >/dev/null

# 2. Root CA — generate only if none present (generate/internal is not idempotent).
if api GET /v1/pki/cert/ca 2>/dev/null | grep -q 'BEGIN CERTIFICATE'; then
    echo "exists  root CA"
else
    api POST /v1/pki/root/generate/internal \
        '{"common_name":"Stratt Dev Root CA","ttl":"87600h"}' >/dev/null
    echo "created root CA (Stratt Dev Root CA)"
fi

# 3. Issuing role: leaf certs under *.stratt.test, up to 90d.
api POST "/v1/pki/roles/$role" \
    '{"allowed_domains":"stratt.test","allow_subdomains":true,"max_ttl":"2160h","key_type":"rsa","key_bits":2048}' >/dev/null
echo "ensured role $role"

# 4. Seed demo leaf certs — only when the estate has no leaf certs yet. LIST
#    returns the CA plus every issued serial; a fresh mount lists just the CA
#    root serial, so "<= 1 non-CA leaf" means unseeded. We count via the role's
#    issued certs by checking for our demo CNs is awkward (LIST gives serials
#    only), so we gate on a sentinel: issue only if fewer than 2 leaves exist.
leaves=$(api LIST /v1/pki/certs 2>/dev/null | python3 -c '
import json,sys
try:
    keys = json.load(sys.stdin)["data"]["keys"]
except Exception:
    keys = []
print(len(keys))
' 2>/dev/null || echo 0)

if [ "${leaves:-0}" -ge 3 ]; then
    echo "exists  demo leaf certs (${leaves} entries) — skipping issue"
else
    api POST "/v1/pki/issue/$role" \
        '{"common_name":"web.stratt.test","ttl":"720h"}' >/dev/null
    echo "issued  web.stratt.test (ttl 720h, healthy)"
    api POST "/v1/pki/issue/$role" \
        '{"common_name":"api.stratt.test","ttl":"48h"}' >/dev/null
    echo "issued  api.stratt.test (ttl 48h, expiring)"
fi

echo "openbao-bootstrap: done"
