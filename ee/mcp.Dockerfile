# MCP execution environment (ADR-0022): the sandbox where the `mcp`
# Actuator's driver runs — and, for stdio transport, where the Git-declared
# server script runs. Deliberately minimal (§1.4, §7.3): python + httpx
# (the http transport's only dependency; stdio needs stdlib alone). The
# Python MCP SDK is deliberately absent (dependency-scout: its mandatory
# ASGI server stack is dead attack surface in a sandbox).
FROM python:3.14-alpine

COPY --from=ghcr.io/astral-sh/uv:0.9 /uv /usr/local/bin/uv
RUN uv pip install --system --no-cache "httpx>=0.28,<0.29"

RUN addgroup -g 1000 runner && adduser -D -u 1000 -G runner runner \
    && mkdir -p /runner/project /runner/artifacts \
    && chown -R runner:runner /runner
USER runner:runner
WORKDIR /runner
