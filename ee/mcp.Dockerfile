# syntax=docker/dockerfile:1
# MCP execution environment (ADR-0022/0053): the sandbox where the stratt-mcp shim
# runs and — for stdio transport — where the Git-declared server script runs.
# Deliberately minimal (§1.4, §7.3). After ADR-0053 the MCP client PROTOCOL is the
# Go stratt-mcp shim (JSON-RPC + the http transport in Go, net/http) — so httpx is
# gone; python remains ONLY to run the untrusted stdio server script (python3
# server.py), sandboxed in this ephemeral pod. The Python MCP SDK stays absent
# (dependency-scout: its mandatory ASGI server stack is dead attack surface here).

# ── Build the stratt-mcp shim (ADR-0053): the MCP client protocol, Apache-2.0 Go ──
FROM golang:1.26 AS shim
WORKDIR /src
COPY sdk/ sdk/
COPY plugins/mcp/ plugins/mcp/
WORKDIR /src/plugins/mcp
ENV GOWORK=off CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build go build -trimpath -o /out/stratt-mcp ./cmd/stratt-mcp

FROM python:3.14-alpine

# The port-speaking shim: the EE-mcp Job's command. It reads /runner/stratt/request.json
# (the sovereign ApplyRequest) and speaks MCP to the declared server.
COPY --from=shim /out/stratt-mcp /usr/local/bin/stratt-mcp

RUN addgroup -g 1000 runner && adduser -D -u 1000 -G runner runner \
    && mkdir -p /runner/project /runner/artifacts /runner/stratt \
    && chown -R runner:runner /runner
USER runner:runner
WORKDIR /runner
