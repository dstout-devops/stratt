# syntax=docker/dockerfile:1
# stratt-plugin-kubecontainers — the K8s container-image Syncer plugin over the sovereign port
# (ADR-0046/0080). A long-lived gRPC service the control plane dials; it reads running
# Pods and projects the software.container image inventory onto the node (host) that runs
# them, feeding the one form-agnostic software-advisory check. The K8s client resolves
# in-cluster (ServiceAccount) or via KUBECONFIG. Apache-2.0 Go; no copyleft; imports
# nothing from core/.
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
COPY plugins/kubecontainers/ plugins/kubecontainers/
WORKDIR /src/plugins/kubecontainers
ENV GOWORK=off CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build go build -trimpath -o /out/stratt-plugin-kubecontainers ./cmd/stratt-plugin-kubecontainers

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratt-plugin-kubecontainers /usr/local/bin/stratt-plugin-kubecontainers
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/stratt-plugin-kubecontainers"]
