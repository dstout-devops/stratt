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

base=${STRATT_OPENBAO_ADDR:-http://localhost:8200}
token=${STRATT_OPENBAO_TOKEN:-stratt-dev-root}
role=${STRATT_OPENBAO_ROLE:-stratt-dev}

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

# 5. KV v2 secrets engine at /secret + a demo secret for the SecretBroker vault
#    backend (ADR-0094). A plugin resolving a CredentialRef{backend: vault,
#    locator: {mount:"secret", path:"demo/aws", kvV2:true}} reads THIS secret — the
#    per-call credential seam, proven against the real OpenBao (never a mock).
if [ "$(probe GET /v1/sys/mounts/secret/tune)" = "200" ]; then
    echo "exists  kv-v2 secrets engine (secret/)"
else
    api POST /v1/sys/mounts/secret '{"type":"kv","options":{"version":"2"}}' >/dev/null
    echo "created kv-v2 secrets engine (secret/)"
fi
# KV v2 writes wrap fields under {"data": {...}}. Idempotent (a re-put overwrites).
api POST /v1/secret/data/demo/aws \
    '{"data":{"access_key":"AKIADEMOKEY000000000","secret_key":"dev/secret/material+never/logged","note":"SecretBroker vault demo (ADR-0094)"}}' >/dev/null
echo "seeded  secret/demo/aws (access_key + secret_key) for the vault SecretBroker"

echo "openbao-bootstrap: done"

# 6. KV metadata-reader policy (ADR-0099 defense-in-depth, guardian finding 1): the
#    KV Syncer's token should read secret/metadata/* and be DENIED secret/data/*, so
#    the never-read-values invariant holds at the ACL layer too (not only the client's
#    no-data-method). Dev runs the plugin on the root token; production wires a token
#    with THIS policy.
if [ "$(probe GET /v1/sys/policies/acl/kv-metadata-reader)" = "200" ]; then
    echo "exists  kv-metadata-reader policy"
else
    api PUT /v1/sys/policies/acl/kv-metadata-reader \
        '{"policy":"path \"secret/metadata/*\" { capabilities = [\"read\",\"list\"] }\npath \"secret/data/*\" { capabilities = [\"deny\"] }"}' >/dev/null
    echo "created kv-metadata-reader policy (read metadata, DENY data)"
fi

# 7. Transit secrets engine for the KeyCustodian capability (ADR-0100): envelope-
#    encryption key wrapping where the KEK never leaves OpenBao. The plugin auto-creates
#    per-domain keys on first wrap; here we just enable the engine.
if [ "$(probe GET /v1/sys/mounts/transit/tune)" = "200" ]; then
    echo "exists  transit secrets engine"
else
    api POST /v1/sys/mounts/transit '{"type":"transit"}' >/dev/null
    echo "created transit secrets engine (KeyCustodian, ADR-0100)"
fi

# 8. identity/oidc workload-identity provider (ADR-0101 Phase B): OpenBao mints signed
#    OIDC ID tokens for its entities, which the strattd multi-issuer OIDCResolver verifies
#    → Principal. This is the SOVEREIGN identity floor — a cut-off Cell mints its own
#    workload identities locally (ADR-0100 alignment).
#
#    EMPIRICAL (proven in-repo): the token `sub` is the entity UUID (non-reassignable ⇒
#    I-4 by construction), NOT the entity name; `iss` = <addr>/v1/identity/oidc. So the
#    Stratt Principal is `openbao/<entity-uuid>` (the resolver prepends the issuer
#    namespace) — this script PRINTS the uuid so CaC tuples / the live proof can grant it.
#
#    IN-CLUSTER this uses OpenBao **Kubernetes auth** (a pod's projected SA token, audience-
#    restricted to OpenBao, I-6 → its entity). Compose has no K8s, so dev uses a **userpass**
#    alias as the login stand-in; the identity/oidc half (key/role/token) is IDENTICAL to prod.
echo "openbao-bootstrap: identity/oidc workload identity (ADR-0101)"

# 8a. Named RS256 signing key + role. allowed_client_ids "*" for dev; prod pins the audience.
api POST /v1/identity/oidc/key/stratt \
    '{"allowed_client_ids":["*"],"algorithm":"RS256","rotation_period":"24h","verification_ttl":"24h"}' >/dev/null
echo "ensured identity/oidc key stratt (RS256)"
api POST /v1/identity/oidc/role/stratt \
    '{"key":"stratt","ttl":"10m","client_id":"stratt"}' >/dev/null
echo "ensured identity/oidc role stratt (aud=stratt, ttl=10m)"

# 8b. Mint policy: read the role's token endpoint (the default policy does NOT grant this).
#     Prod attaches this to the K8s-auth role's token_policies; dev to the userpass user.
if [ "$(probe GET /v1/sys/policies/acl/stratt-oidc-mint)" = "200" ]; then
    echo "exists  stratt-oidc-mint policy"
else
    api PUT /v1/sys/policies/acl/stratt-oidc-mint \
        '{"policy":"path \"identity/oidc/token/stratt\" { capabilities = [\"read\"] }"}' >/dev/null
    echo "created stratt-oidc-mint policy"
fi

# 8c. Demo workload entity 'svc-syncer'. Its UUID (not its name) becomes the Principal.
eid=$(api POST /v1/identity/entity \
        '{"name":"svc-syncer","metadata":{"stratt_role":"syncer"}}' 2>/dev/null \
      | python3 -c 'import json,sys
try: print(json.load(sys.stdin)["data"]["id"])
except Exception: print("")' 2>/dev/null)
if [ -z "$eid" ]; then
    # Already exists (POST returns no body on update) — look it up by name.
    eid=$(api GET /v1/identity/entity/name/svc-syncer 2>/dev/null \
          | python3 -c 'import json,sys; print(json.load(sys.stdin)["data"]["id"])' 2>/dev/null || echo "")
fi
echo "ensured entity svc-syncer -> Principal openbao/${eid}"

# 8d. Userpass login stand-in (dev only; prod = K8s auth). Alias binds the login → entity.
api POST /v1/sys/auth/userpass '{"type":"userpass"}' >/dev/null 2>&1 || true
accessor=$(api GET /v1/sys/auth 2>/dev/null \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["data"]["userpass/"]["accessor"])' 2>/dev/null || echo "")
api POST /v1/auth/userpass/users/svc-syncer \
    '{"password":"devpw","token_policies":"stratt-oidc-mint","token_ttl":"10m"}' >/dev/null
if [ -n "$accessor" ] && [ -n "$eid" ]; then
    api POST /v1/identity/entity-alias \
        "{\"name\":\"svc-syncer\",\"canonical_id\":\"$eid\",\"mount_accessor\":\"$accessor\"}" >/dev/null 2>&1 || true
    echo "ensured userpass login svc-syncer (dev stand-in for K8s auth, I-6)"
fi

# 8e. Report the issuer URL the resolver must trust (STRATT_OIDC_ISSUERS[].issuer).
issuer=$(curl -sS "$base/v1/identity/oidc/.well-known/openid-configuration" 2>/dev/null \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["issuer"])' 2>/dev/null || echo "")
echo "identity/oidc issuer: ${issuer:-<addr>/v1/identity/oidc}  (aud=stratt, subNamespace=openbao/)"

echo "openbao-bootstrap: done"
