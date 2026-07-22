#!/usr/bin/env bash
# Container-only: teach `gh` to borrow the token VSCode already forwards for `git`, so the GitHub CLI
# works with NO stored or committed secret. The dev-container credential helper serves `git` a GitHub
# OAuth token; `gh` reuses it via GH_TOKEN. (`gh auth login --with-token` is rejected for missing the
# 'read:org' scope on VSCode's OAuth token; the GH_TOKEN env path skips that login-time gate.)
#
# Idempotent — safe to re-run. Appends a live-derivation snippet to the interactive shell rc files, so
# every new shell (including Claude Code's Bash tool, which initializes from the profile) re-derives
# the token from the same credential VSCode already hands `git`. See docs/mcp-servers.md.
set -euo pipefail

marker="# >>> stratt gh auto-auth >>>"
read -r -d '' snippet <<'EOF' || true
# >>> stratt gh auto-auth >>>
# Borrow the VSCode-forwarded git credential as gh's token — nothing stored on disk or in git.
if command -v gh >/dev/null 2>&1 && [ -z "${GH_TOKEN:-}" ]; then
  export GH_TOKEN="$(printf 'protocol=https\nhost=github.com\n\n' \
    | GIT_TERMINAL_PROMPT=0 git credential fill 2>/dev/null | sed -n 's/^password=//p')"
fi
# <<< stratt gh auto-auth <<<
EOF

for rc in "$HOME/.bashrc" "$HOME/.zshrc"; do
  touch "$rc"
  if grep -qF "$marker" "$rc"; then
    echo "gh auto-auth: already present in $rc"
  else
    printf '\n%s\n' "$snippet" >>"$rc"
    echo "gh auto-auth: snippet added to $rc"
  fi
done
