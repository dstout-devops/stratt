# syntax=docker/dockerfile:1
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

# ── stratt-ansible shim (ADR-0051): the port-speaking binary baked into the EE ──
# Apache-2.0 Go that reads the sovereign ApplyRequest, shells out to ansible-runner
# (the GPL boundary stays a SUBPROCESS — never linked), and emits the port's typed
# shapes as proto-JSON on stdout. The EE-Job transport runs THIS as the pod command;
# the core forwards its typed stream and governs hub-side (GovernStream). Build
# context is the repo root — the module + its only dep (the lean sdk, via replace).
FROM golang:1.26 AS shim
WORKDIR /src
COPY sdk/ sdk/
COPY plugins/ansible/ plugins/ansible/
COPY plugins/script/ plugins/script/
ENV GOWORK=off CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build cd plugins/ansible && go build -trimpath -o /out/stratt-ansible ./cmd/stratt-ansible
# stratt-script (ADR-0046 Category A): the per-target script-runner over the port.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build cd plugins/script && go build -trimpath -o /out/stratt-script ./cmd/stratt-script

FROM python:3.14-slim

# git + ssh client: the ansible Actuator's SCM content-ref (charter §5.6,
# ADR-0025) clones a repo in this pod (the GPL boundary keeps git — like
# Ansible — out of the Go control plane). ca-certificates for https clones.
RUN apt-get update \
    && apt-get install -y --no-install-recommends git ca-certificates openssh-client \
    && rm -rf /var/lib/apt/lists/*

# uv is the only sanctioned Python package manager (.claude/rules/backend-python.md).
COPY --from=ghcr.io/astral-sh/uv:0.9 /uv /usr/local/bin/uv

RUN uv pip install --system --no-cache \
    "ansible-core>=2.19,<2.20" \
    "ansible-runner>=2.4,<3"

# The port-speaking shims (ADR-0051/0046): the EE-Job transport's pod commands. Each
# reads /runner/stratt/request.json (the sovereign ApplyRequest) and emits typed
# ApplyResponses. stratt-ansible runs ansible-runner; stratt-script runs user scripts.
COPY --from=shim /out/stratt-ansible /usr/local/bin/stratt-ansible
COPY --from=shim /out/stratt-script /usr/local/bin/stratt-script

# Non-root: the dispatcher's pods run unprivileged.
RUN useradd --create-home --uid 1000 runner \
    && mkdir -p /runner/project /runner/inventory /runner/artifacts /runner/stratt \
    && chown -R runner:runner /runner
USER runner
WORKDIR /runner

# The dispatcher overrides the command: the EE-Job transport (ADR-0051) sets
# ["stratt-ansible"]; this default documents the legacy in-tree-actuator contract.
CMD ["ansible-runner", "run", "/runner", "-p", "play.yml", "-j"]
