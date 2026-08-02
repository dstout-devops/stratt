#!/bin/sh
# Drive the SCIM 2.0 Service Provider like a real IdP would (ADR-0035): push two
# Users, a Group with one member, reconcile by filter, then deactivate a user.
# Stratt is the SP; this script is the fake IdP CLIENT (the inverted-graphsim
# posture — the sim pushes to us). Re-runnable against a live strattd.
#
# The bearer token must match the sha256 in the registered `scim` IdP CaC doc
# (declarations authz/../scim/<name>.yaml). Compute the hash for a token with:
#   printf %s "$STRATT_SCIM_TOKEN" | sha256sum
#
# Usage: STRATT_SCIM_TOKEN=... deploy/dev/scim-bootstrap.sh
#        (or: task dev:scim:bootstrap)
set -eu

base=${STRATT_SERVER:-http://localhost:8080}
token=${STRATT_SCIM_TOKEN:?set STRATT_SCIM_TOKEN to the IdP bearer token}

scim() {
    method=$1
    path=$2
    body=${3:-}
    curl -fsS -X "$method" "$base/scim/v2$path" \
        -H "Authorization: Bearer $token" \
        -H "Content-Type: application/scim+json" \
        ${body:+-d "$body"}
}

echo "== ServiceProviderConfig =="
scim GET /ServiceProviderConfig | jq '{patch:.patch.supported, filter:.filter.supported}'

echo "== provision users =="
alice=$(scim POST /Users '{
  "schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],
  "userName":"alice@stratt.test","externalId":"sub-alice","active":true,
  "emails":[{"value":"alice@stratt.test","primary":true}]
}' | jq -r .id)
bob=$(scim POST /Users '{
  "schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],
  "userName":"bob@stratt.test","externalId":"sub-bob","active":true
}' | jq -r .id)
echo "alice=$alice bob=$bob"

echo "== provision group 'Platform Eng' with alice =="
gid=$(scim POST /Groups "{
  \"schemas\":[\"urn:ietf:params:scim:schemas:core:2.0:Group\"],
  \"displayName\":\"Platform Eng\",
  \"members\":[{\"value\":\"$alice\"}]
}" | jq -r .id)
echo "group=$gid"

echo "== add bob to the group (PATCH) =="
scim PATCH "/Groups/$gid" "{
  \"schemas\":[\"urn:ietf:params:scim:api:messages:2.0:PatchOp\"],
  \"Operations\":[{\"op\":\"add\",\"path\":\"members\",\"value\":[{\"value\":\"$bob\"}]}]
}" >/dev/null
echo "ok"

echo "== reconcile by filter (userName eq) =="
scim GET '/Users?filter=userName%20eq%20%22alice@stratt.test%22' | jq '{total:.totalResults, first:.Resources[0].userName}'

echo "== deactivate bob (PATCH active:false — the offboarding trigger) =="
scim PATCH "/Users/$bob" '{
  "schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
  "Operations":[{"op":"replace","path":"active","value":false}]
}' | jq '{userName, active}'

echo
echo "Done. Within a reconcile interval, alice (active, in the mapped group) gains"
echo "her team's grants; bob's are revoked and his requests are blocked at resolve."
