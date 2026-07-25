# syntax=docker/dockerfile:1
# stratt-plugin-vcenter — the vCenter Syncer plugin over the sovereign port
# (ADR-0046). A long-lived gRPC service the control plane dials
# (STRATT_VCENTER_PLUGIN_ADDR); it enumerates the vCenter/vcsim inventory with
# govmomi and streams typed ObservedEntities back, governed hub-side. Apache-2.0
# Go; no copyleft.
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
COPY plugins/vcenter/ plugins/vcenter/
WORKDIR /src/plugins/vcenter
ENV GOWORK=off CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build go build -trimpath -o /out/stratt-plugin-vcenter ./cmd/stratt-plugin-vcenter

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratt-plugin-vcenter /usr/local/bin/stratt-plugin-vcenter
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/stratt-plugin-vcenter"]
