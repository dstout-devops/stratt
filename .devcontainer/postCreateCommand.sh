#!/usr/bin/env bash
# Devcontainer bootstrap — runs once after the container is created.
# Delegates to the task runner (charter §3) so the bootstrap logic lives in one
# place (Taskfile.yml) and is reproducible outside the devcontainer too.
set -euo pipefail

WORKSPACE="${WORKSPACE_ROOT:-$PWD}"

echo "Stratt devcontainer post-create bootstrap"
task --dir "$WORKSPACE" setup:devcontainer

# Teach gh to borrow the forwarded git credential (container-only; no stored/committed secret).
task --dir "$WORKSPACE" setup:gh-auth
