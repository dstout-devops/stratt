# syntax=docker/dockerfile:1
# stratt-plugin-awsec2 — the EC2 plugin over the sovereign port (ADR-0046/0031):
# a create-vm build Action + an instance Syncer. The control plane dials it
# (STRATT_AWS_PLUGIN_ADDR) and governs what it may write. Apache-2.0 Go; no
# copyleft; imports nothing from core/.
#
# Build context is the repo root — the module + its deps (the lean sdk).
FROM golang:1.26 AS build
# Bound compile fan-out (same lever + default as the Taskfile's host gates). Without it a
# cold cache compiles the whole dependency graph at -p=NumCPU, which on a many-core host
# over overlayfs corrupts the build cache it is writing (`internal compiler error: export
# data desync` in a random dependency) and can exhaust host memory outright.
ARG GO_PARALLEL=8
ENV GOFLAGS=-p=${GO_PARALLEL}
WORKDIR /src
COPY sdk/ sdk/
COPY plugins/awsec2/ plugins/awsec2/
WORKDIR /src/plugins/awsec2
ENV GOWORK=off CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build go build -trimpath -o /out/stratt-plugin-awsec2 ./cmd/stratt-plugin-awsec2

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratt-plugin-awsec2 /usr/local/bin/stratt-plugin-awsec2
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/stratt-plugin-awsec2"]
