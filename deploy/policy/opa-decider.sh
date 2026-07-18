#!/usr/bin/env bash
# opa-decider.sh — the subprocess transport for an OPA-backed Policy Decision
# Point (ADR-0074). strattd runs `opa-decider.sh <op>` with the request JSON on
# stdin (op = decide|admit) and reads a Decision JSON on stdout.
#
# The Decision contract is emitted by the Rego package `stratt` (see policy.rego):
#   data.stratt.decide  ← the gate PEP    (input = {controls, context})
#   data.stratt.admit   ← the admission PEP (input = {object, controls})
#
# Requires `opa` and `jq` on PATH (ship them in the EE/policy image, not core).
set -euo pipefail

op="${1:?usage: opa-decider.sh <decide|admit>}"
bundle="${STRATT_OPA_BUNDLE:-/etc/stratt/policy}"

# opa eval binds stdin to `input`; we extract just the decision value so stdout
# is exactly the Decision JSON Stratt expects.
opa eval --format=json --stdin-input --data "$bundle" "data.stratt.${op}" \
  | jq -ec '.result[0].expressions[0].value'
