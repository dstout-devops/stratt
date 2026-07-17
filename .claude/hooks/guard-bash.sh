#!/usr/bin/env bash
# PreToolUse(Bash) guard — hard-blocks destructive commands regardless of what the model decides.
# Belt-and-suspenders with permissions.deny; hooks cannot be talked around. See charter guardrails.
set -euo pipefail

input="$(cat)"
cmd="$(printf '%s' "$input" | jq -r '.tool_input.command // ""')"

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

# Destructive / dangerous patterns.
case "$cmd" in
  *"rm -rf /"*|*"rm -rf ~"*|*"rm -fr /"*)          deny "Blocked: recursive force-delete of a root/home path." ;;
  *"sudo "*)                                        deny "Blocked: sudo is not permitted in this environment." ;;
  *"git push --force"*|*"git push -f"*)             deny "Blocked: force-push. Use --force-with-lease intentionally and by hand." ;;
  *"git reset --hard"*)                             deny "Blocked: 'git reset --hard' can destroy work. Use checkpoints/stash, or run it yourself." ;;
  *"git clean -"*[fdx]*)                            deny "Blocked: 'git clean -fdx' deletes untracked files. Run it yourself if intended." ;;
  *"git checkout -- ."*|*"git checkout ."*|*"git restore ."*|*"git restore -- ."*) \
    deny "Blocked: whole-tree 'git checkout .'/'git restore .' can silently discard uncommitted work. Check 'git status' and stash first, or run it yourself." ;;
  *"chmod -R 777"*|*"chmod 777"*)                   deny "Blocked: world-writable chmod." ;;
  *"mkfs"*|*"dd if="*of=/dev/*)                     deny "Blocked: raw disk write." ;;
  *"curl "*"| sh"*|*"curl "*"| bash"*|*"wget "*"| sh"*|*"wget "*"| bash"*) deny "Blocked: piping a remote script straight into a shell. Download, review, then run." ;;
  *":(){ :|:& };:"*)                                deny "Blocked: fork bomb." ;;
esac

# Protect the design authority and license from shell-based edits (>, >>, sed -i, tee, truncate).
case "$cmd" in
  *stratt-charter.md*|*LICENSE*)
    case "$cmd" in
      *">"*|*">>"*|*"sed -i"*|*"tee "*|*"truncate"*|*"mv "*|*"rm "*)
        deny "Blocked: shell modification of LICENSE/stratt-charter.md. These change only by explicit instruction." ;;
    esac ;;
esac

exit 0
