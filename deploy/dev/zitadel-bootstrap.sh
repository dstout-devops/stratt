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

# ── human user for interactive (authorization-code) login — UI slice, ADR-0012
human=stratt-e2e-human
human_pass=${STRATT_E2E_HUMAN_PASSWORD:-Sup3r-Str4tt-Dev!}
hsub=$(api POST /management/v1/users/_search \
    "{\"queries\":[{\"userNameQuery\":{\"userName\":\"$human\",\"method\":\"TEXT_QUERY_METHOD_EQUALS\"}}]}" |
    field id)
if [ -z "$hsub" ]; then
    hsub=$(api POST /management/v1/users/human/_import \
        "{\"userName\":\"$human\",\"profile\":{\"firstName\":\"Stratt\",\"lastName\":\"Human\"},\"email\":{\"email\":\"human@stratt.localhost\",\"isEmailVerified\":true},\"password\":\"$human_pass\",\"passwordChangeRequired\":false}" |
        field userId)
    echo "created $human (sub $hsub)"
else
    echo "exists  $human (sub $hsub)"
fi
echo "STRATT_E2E_HUMAN_SUB=$hsub" >>"$out_file"

# ── SPA OIDC app (PKCE) the UI logs in with — ADR-0012
proj=$(api POST /management/v1/projects/_search \
    '{"queries":[{"nameQuery":{"name":"stratt","method":"TEXT_QUERY_METHOD_EQUALS"}}]}' |
    field id)
if [ -z "$proj" ]; then
    proj=$(api POST /management/v1/projects '{"name":"stratt"}' | field id)
    echo "created project stratt ($proj)"
else
    echo "exists  project stratt ($proj)"
fi
ui_client=$(api POST "/management/v1/projects/$proj/apps/_search" \
    '{"queries":[{"nameQuery":{"name":"stratt-ui","method":"TEXT_QUERY_METHOD_EQUALS"}}]}' |
    field clientId)
if [ -z "$ui_client" ]; then
    # USER_AGENT + auth method NONE = SPA with PKCE; devMode allows http
    # redirects (dev instance only); JWT access tokens verify via JWKS
    # (slice 5) so the same Bearer works against strattd.
    ui_client=$(api POST "/management/v1/projects/$proj/apps/oidc" \
        '{"name":"stratt-ui","redirectUris":["http://localhost:5173/callback","http://localhost:8080/callback"],"postLogoutRedirectUris":["http://localhost:5173/","http://localhost:8080/"],"responseTypes":["OIDC_RESPONSE_TYPE_CODE"],"grantTypes":["OIDC_GRANT_TYPE_AUTHORIZATION_CODE","OIDC_GRANT_TYPE_REFRESH_TOKEN"],"appType":"OIDC_APP_TYPE_USER_AGENT","authMethodType":"OIDC_AUTH_METHOD_TYPE_NONE","accessTokenType":"OIDC_TOKEN_TYPE_JWT","devMode":true}' |
        field clientId)
    echo "created app stratt-ui (client $ui_client)"
else
    echo "exists  app stratt-ui (client $ui_client)"
fi
echo "STRATT_UI_CLIENT_ID=$ui_client" >>"$out_file"

echo
echo "wrote $out_file — mint a token with:"
echo "  curl -s $base/oauth/v2/token -u '<client_id>:<client_secret>' \\"
echo "    -d grant_type=client_credentials -d scope=openid"
echo "grant in tuples.yaml against: principal:<sub>"
