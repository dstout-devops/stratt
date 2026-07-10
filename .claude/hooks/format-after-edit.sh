#!/usr/bin/env bash
# PostToolUse(Write|Edit) — best-effort auto-format of the edited file.
# Defensive: no-ops silently if the relevant formatter isn't installed yet (pre-Phase-0 there is no
# toolchain). Never fails the turn. Add project-specific formatters here as the stack lands.
set -uo pipefail

input="$(cat)"
file="$(printf '%s' "$input" | jq -r '.tool_input.file_path // ""')"
[ -f "$file" ] || exit 0

have() { command -v "$1" >/dev/null 2>&1; }

case "$file" in
  *.py)
    if have ruff; then ruff format "$file" >/dev/null 2>&1 || true; ruff check --fix "$file" >/dev/null 2>&1 || true; fi
    ;;
  *.ts|*.tsx|*.js|*.jsx|*.css|*.json|*.md)
    if have prettier;      then prettier --write "$file" >/dev/null 2>&1 || true
    elif have npx;         then npx --no-install prettier --write "$file" >/dev/null 2>&1 || true; fi
    ;;
  *.go)
    if have gofmt; then gofmt -w "$file" >/dev/null 2>&1 || true; fi
    ;;
esac

exit 0
