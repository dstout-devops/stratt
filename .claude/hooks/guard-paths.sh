#!/usr/bin/env bash
# PreToolUse(Write|Edit) guard — hard-blocks writes to protected paths.
# Protects the design authority (charter), LICENSE, git internals, and secret files.
set -euo pipefail

input="$(cat)"
path="$(printf '%s' "$input" | jq -r '.tool_input.file_path // ""')"

deny() {
  jq -n --arg r "$1" '{
    hookSpecificOutput: {
      hookEventName: "PreToolUse",
      permissionDecision: "deny",
      permissionDecisionReason: $r
    }
  }'
  exit 0
}

base="$(basename "$path")"

case "$path" in
  */.git/*)                                deny "Blocked: writing inside .git/." ;;
  */secrets/*)                             deny "Blocked: writing under a secrets/ directory." ;;
esac

case "$base" in
  LICENSE)                                 deny "Blocked: LICENSE changes only by explicit instruction (charter §1.3)." ;;
  stratt-charter.md)                       deny "Blocked: stratt-charter.md is the design authority; edit only when explicitly told (charter §0/§1)." ;;
  .env|.env.*)                             deny "Blocked: writing an env/secret file." ;;
  *.pem|*.key|id_rsa|id_ed25519)           deny "Blocked: writing a private-key/secret file." ;;
esac

exit 0
