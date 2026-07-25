# syntax=docker/dockerfile:1
# stratt-notify — the notification-delivery plugin Action (ADR-0046/0052). A long-lived
# gRPC service the control plane dials (STRATT_NOTIFY_PLUGIN_ADDR); it resolves the
# Sink's per-call url/token via the SecretBroker (in-cluster K8s Secret reads under its
# OWN confined RBAC, MF-A) and issues the HTTP POST. Apache-2.0 Go; no copyleft.
#
# Build context is the repo root — the module + its deps (the lean sdk + sdk/secretbroker).
FROM golang:1.26 AS build
# Bound compile fan-out (same lever + default as the Taskfile's host gates). Without it a
# cold cache compiles the whole dependency graph at -p=NumCPU, which on a many-core host
# over overlayfs corrupts the build cache it is writing (`internal compiler error: export
# data desync` in a random dependency) and can exhaust host memory outright.
ARG GO_PARALLEL=8
ENV GOFLAGS=-p=${GO_PARALLEL}
WORKDIR /src
COPY sdk/ sdk/
COPY plugins/notify/ plugins/notify/
WORKDIR /src/plugins/notify
ENV GOWORK=off CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build go build -trimpath -o /out/stratt-notify ./cmd/stratt-notify

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratt-notify /usr/local/bin/stratt-notify
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/stratt-notify"]
