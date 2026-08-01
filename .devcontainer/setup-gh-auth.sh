#!/usr/bin/env bash
# Container-only: teach `gh` to borrow the token VSCode already forwards for `git`, so the GitHub CLI
# works with NO stored or committed secret. The dev-container credential helper serves `git` a GitHub
# OAuth token; `gh` reuses it via GH_TOKEN. (`gh auth login --with-token` is rejected for missing the
# 'read:org' scope on VSCode's OAuth token; the GH_TOKEN env path skips that login-time gate.)
#
# Installs a lazy `gh` WRAPPER FUNCTION (not an eager export): the credential helper is called ONLY the
# first time `gh` is invoked in a shell — no per-command latency, nothing on disk. Critically it lands
# in ~/.zshenv, which zsh sources for EVERY invocation including NON-interactive ones — so Claude Code's
# Bash tool (a non-interactive zsh that does NOT source ~/.zshrc) gets `gh` auth too, not just terminals.
#
# Idempotent — safe to re-run; it strips any prior block before re-adding.
set -euo pipefail

begin="# >>> stratt gh auto-auth >>>"
end="# <<< stratt gh auto-auth <<<"

read -r -d '' block <<EOF || true
${begin}
# Lazily borrow VSCode's forwarded git credential as gh's token, only when gh is first used — nothing
# stored on disk or in git. (gh auth login --with-token is rejected for lacking read:org; GH_TOKEN skips it.)
gh() {
  if [ -z "\${GH_TOKEN:-}" ]; then
    GH_TOKEN="\$(printf 'protocol=https\\nhost=github.com\\n\\n' \\
      | GIT_TERMINAL_PROMPT=0 git credential fill 2>/dev/null | sed -n 's/^password=//p')"
    [ -n "\$GH_TOKEN" ] && export GH_TOKEN
  fi
  command gh "\$@"
}
${end}
EOF

# Remove any existing marker block so a re-run UPDATES (older versions wrote an eager export to
# ~/.zshrc/~/.bashrc; strip those too), then re-add the current wrapper.
strip_block() {
  local rc="$1"
  [ -f "$rc" ] || return 0
  sed -i "/^${begin}$/,/^${end}$/d" "$rc"
}

# ~/.zshenv — ALL zsh (interactive + non-interactive; covers the Claude Code Bash tool).
# ~/.bashrc — interactive bash terminals.
# ~/.zshrc  — clean only: superseded by ~/.zshenv for zsh (prevents a stale/duplicate block).
for rc in "$HOME/.zshenv" "$HOME/.bashrc" "$HOME/.zshrc"; do
  strip_block "$rc"
done
for rc in "$HOME/.zshenv" "$HOME/.bashrc"; do
  touch "$rc"
  printf '\n%s\n' "$block" >>"$rc"
  echo "gh auto-auth: installed the lazy gh wrapper in $rc"
done
echo "gh auto-auth: done (non-interactive shells covered via ~/.zshenv)"
