#!/bin/sh
# Provision the dev e2e service identities in the compose Zitadel (ADR-0009
# slice 5). Idempotent: existing users are reused; client secrets are
# (re)generated every run and written to deploy/dev/.zitadel/identities.env
# (gitignored — dev-instance-local, like the bootstrap PAT).
#
# The printed `sub` values are what authz tuples grant against
# (principal:<sub> in the declarations repo's authz/tuples.yaml).
#
# Usage: deploy/dev/zitadel-bootstrap.sh  (or: task dev:zitadel:bootstrap)
set -eu

here=$(dirname "$0")
base=${STRATT_ZITADEL_URL:-http://localhost:8082}
pat_file="$here/.zitadel/admin.pat"
out_file="$here/.zitadel/identities.env"

[ -f "$pat_file" ] || {
    echo "zitadel-bootstrap: $pat_file not found — is the substrate up (task dev:up)?" >&2
    exit 1
}
pat=$(cat "$pat_file")

api() {
    method=$1
    path=$2
    body=${3:-}
    curl -fsS -X "$method" "$base$path" \
        -H "Authorization: Bearer $pat" \
        -H "Content-Type: application/json" \
        ${body:+-d "$body"}
}

# jq-free JSON field extraction — fine for Zitadel's flat id/secret payloads.
field() {
    sed -n "s/.*\"$1\":\"\\([^\"]*\\)\".*/\\1/p" | head -1
}

: >"$out_file"
for user in stratt-e2e-admin stratt-e2e-intern stratt-e2e-outsider; do
    sub=$(api POST /management/v1/users/_search \
        "{\"queries\":[{\"userNameQuery\":{\"userName\":\"$user\",\"method\":\"TEXT_QUERY_METHOD_EQUALS\"}}]}" |
        field id)
    if [ -z "$sub" ]; then
        # accessTokenType JWT: go-oidc verifies via JWKS; opaque would need introspection.
        sub=$(api POST /management/v1/users/machine \
            "{\"userName\":\"$user\",\"name\":\"$user\",\"accessTokenType\":\"ACCESS_TOKEN_TYPE_JWT\"}" |
            field userId)
        echo "created $user (sub $sub)"
    else
        echo "exists  $user (sub $sub)"
    fi
    secret=$(api PUT "/management/v1/users/$sub/secret" "{}")
    client_id=$(echo "$secret" | field clientId)
    client_secret=$(echo "$secret" | field clientSecret)
    varname=$(echo "$user" | tr 'a-z-' 'A-Z_')
    {
        echo "${varname}_SUB=$sub"
        echo "${varname}_CLIENT_ID=$client_id"
        echo "${varname}_CLIENT_SECRET=$client_secret"
    } >>"$out_file"
done

echo
echo "wrote $out_file — mint a token with:"
echo "  curl -s $base/oauth/v2/token -u '<client_id>:<client_secret>' \\"
echo "    -d grant_type=client_credentials -d scope=openid"
echo "grant in tuples.yaml against: principal:<sub>"
