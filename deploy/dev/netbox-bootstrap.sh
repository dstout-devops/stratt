#!/bin/sh
# Seed the compose NetBox with a realistic ENTERPRISE topology (ADR-0111): regions,
# availability zones (sites), sovereignty (tenants), a VLAN group, and the container
# supernet the netbox plugin's ipam-resolve carves /24 prefixes from. This is what
# lets Stratt allocate against true enterprise scenarios rather than a flat pool.
#
# Idempotent: each object is created only if absent (safe to re-run after every
# `task dev:up`, the way openbao-bootstrap.sh re-seeds the dev CA). The API token is
# the known dev SUPERUSER_API_TOKEN set in docker-compose.yml (dev-only; real
# deployments broker it via CredentialRef, §2.5).
#
# Usage: deploy/dev/netbox-bootstrap.sh   (or: task dev:netbox:bootstrap)
set -eu

base=${STRATT_NETBOX_ADDR:-http://localhost:8083}
token=${STRATT_NETBOX_TOKEN:-0123456789abcdef0123456789abcdef01234567}

api() {
	method=$1
	path=$2
	body=${3:-}
	curl -fsS -X "$method" "$base/api$path" \
		-H "Authorization: Token $token" \
		-H "Content-Type: application/json" \
		-H "Accept: application/json" \
		${body:+-d "$body"}
}

# count <list-path?filter> → the NetBox result count (0 when absent).
count() {
	api GET "$1" | sed -n 's/.*"count"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' | head -n1
}

# ensure <label> <check-path> <create-path> <create-body>: create only if absent.
ensure() {
	n=$(count "$2")
	if [ "${n:-0}" -gt 0 ]; then
		echo "  ~ $1 exists"
	else
		api POST "$3" "$4" >/dev/null
		echo "  + $1 created"
	fi
}

echo "Seeding NetBox enterprise topology at $base ..."

# Regions (geographic hierarchy).
ensure "region eu-west" "/dcim/regions/?slug=eu-west" "/dcim/regions/" '{"name":"EU West","slug":"eu-west"}'
ensure "region us-east" "/dcim/regions/?slug=us-east" "/dcim/regions/" '{"name":"US East","slug":"us-east"}'

# Availability zones (sites within a region).
ensure "site eu-west-1a" "/dcim/sites/?slug=eu-west-1a" "/dcim/sites/" '{"name":"eu-west-1a","slug":"eu-west-1a","region":{"slug":"eu-west"}}'
ensure "site eu-west-1b" "/dcim/sites/?slug=eu-west-1b" "/dcim/sites/" '{"name":"eu-west-1b","slug":"eu-west-1b","region":{"slug":"eu-west"}}'

# Sovereignty (tenants — jurisdiction boundaries).
ensure "tenant eu-sovereign" "/tenancy/tenants/?slug=eu-sovereign" "/tenancy/tenants/" '{"name":"EU Sovereign","slug":"eu-sovereign"}'
ensure "tenant us-sovereign" "/tenancy/tenants/?slug=us-sovereign" "/tenancy/tenants/" '{"name":"US Sovereign","slug":"us-sovereign"}'

# A VLAN group (scoped VLAN id space).
ensure "vlan-group prod" "/ipam/vlan-groups/?slug=prod" "/ipam/vlan-groups/" '{"name":"prod","slug":"prod"}'

# The container supernet the ipam-resolve carves /24s from. status=container is what
# makes NetBox's available-prefixes endpoint carve children from it. Left tenant-free
# so the pool is carveable regardless of the tenant-scope filter (D5: scope is a
# routing input, not an enforced boundary yet).
ensure "prefix 10.30.0.0/16 (EU app supernet)" "/ipam/prefixes/?prefix=10.30.0.0/16" "/ipam/prefixes/" \
	'{"prefix":"10.30.0.0/16","status":"container","description":"stratt enterprise app supernet (EU)"}'

echo "NetBox seed done. The ipam-resolve Action allocates /24s from 10.30.0.0/16 (pool)."
