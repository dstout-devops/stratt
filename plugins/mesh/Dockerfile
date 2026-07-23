# syntax=docker/dockerfile:1
# stratt-plugin-mesh — the service-mesh dependency Syncer plugin over the sovereign
# port (ADR-0046/0047, ADR-0082 slice 2). A long-lived gRPC service the control plane
# dials; it reads a mesh's request telemetry from a Prometheus-compatible backend
# (STRATT_MESH_PROMETHEUS_URL) and streams `service --depends-on--> service` edges back,
# governed hub-side. The mesh flavor (Istio/Linkerd/Consul/Cilium) is the PromQL query +
# label config, never compiled in. Apache-2.0 Go; no copyleft; imports nothing from
# core/ and no metrics client (the transport is stdlib net/http).
#
# Build context is the repo root — the module + its deps (the lean sdk).
FROM golang:1.26 AS build
WORKDIR /src
COPY sdk/ sdk/
COPY plugins/mesh/ plugins/mesh/
WORKDIR /src/plugins/mesh
ENV GOWORK=off CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build go build -trimpath -o /out/stratt-plugin-mesh ./cmd/stratt-plugin-mesh

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratt-plugin-mesh /usr/local/bin/stratt-plugin-mesh
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/stratt-plugin-mesh"]
