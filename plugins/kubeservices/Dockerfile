# syntax=docker/dockerfile:1
# stratt-plugin-kubeservices — the K8s service/application Syncer (ADR-0081) over the sovereign
# port. It lists Services in scope and projects two dimensions from one scrape: `service` Entities
# with service.endpoint, and form-neutral `application` Entities carrying software.chart (the
# advisory list) and app.deliverable (the scalar a Blueprint drift expectation reads), with the
# `provides` edge between them DERIVED from the app.kubernetes.io/* + helm.sh/chart labels.
#
# It reads the cluster with its own ServiceAccount — a cluster-wide read-only ClusterRole over
# `services` (deploy/charts/stratt values `kubeAccess`), never a kubeconfig baked into the image
# (§2.5). Apache-2.0 Go; the plugin holds no graph write path (§1.2).
#
# Build context is the repo root — the module + its deps (the lean sdk).
FROM golang:1.26 AS build
# Bound compile fan-out (same lever + default as the Taskfile's host gates). Without it a
# cold cache compiles the whole dependency graph at -p=NumCPU, which on a many-core host
# over overlayfs corrupts the build cache it is writing and can exhaust host memory.
ARG GO_PARALLEL=8
ENV GOFLAGS=-p=${GO_PARALLEL}
WORKDIR /src
COPY sdk/ sdk/
COPY plugins/kubeservices/ plugins/kubeservices/
WORKDIR /src/plugins/kubeservices
ENV GOWORK=off CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build go build -trimpath -o /out/stratt-plugin-kubeservices ./cmd/stratt-plugin-kubeservices

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratt-plugin-kubeservices /usr/local/bin/stratt-plugin-kubeservices
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/stratt-plugin-kubeservices"]
