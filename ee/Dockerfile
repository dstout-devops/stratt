# Stratt execution-environment (EE) image — the ansible Actuator's runtime
# (charter §3, ADR-0002). This is the GPLv3 boundary: Ansible lives HERE, in a
# separate image executed as a subprocess in ephemeral K8s Job pods. The Go
# control plane never links it.
#
# ansible-builder EE compatibility is a charter commitment (§3); this dev
# image keeps the same /runner private-data-dir contract ansible-runner
# defines, so ansible-builder-produced EEs are drop-in replacements.
#
# Pins ride the quarterly evergreen train (§1.7).

FROM python:3.14-slim

# uv is the only sanctioned Python package manager (.claude/rules/backend-python.md).
COPY --from=ghcr.io/astral-sh/uv:0.9 /uv /usr/local/bin/uv

RUN uv pip install --system --no-cache \
    "ansible-core>=2.19,<2.20" \
    "ansible-runner>=2.4,<3"

# Non-root: the dispatcher's pods run unprivileged.
RUN useradd --create-home --uid 1000 runner \
    && mkdir -p /runner/project /runner/inventory /runner/artifacts \
    && chown -R runner:runner /runner
USER runner
WORKDIR /runner

# The dispatcher overrides the command; this default documents the contract.
CMD ["ansible-runner", "run", "/runner", "-p", "play.yml", "-j"]
