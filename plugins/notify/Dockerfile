# syntax=docker/dockerfile:1
# stratt-notify — the notification-delivery plugin Action (ADR-0046/0052). A long-lived
# gRPC service the control plane dials (STRATT_NOTIFY_PLUGIN_ADDR); it resolves the
# Sink's per-call url/token via the SecretBroker (in-cluster K8s Secret reads under its
# OWN confined RBAC, MF-A) and issues the HTTP POST. Apache-2.0 Go; no copyleft.
#
# Build context is the repo root — the module + its deps (the lean sdk + sdk/secretbroker).
FROM golang:1.26 AS build
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
