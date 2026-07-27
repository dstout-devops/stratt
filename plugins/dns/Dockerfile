# syntax=docker/dockerfile:1
# stratt-plugin-dns — the DNS Connector/Actuator over the sovereign port
# (ADR-0046/0144). A long-lived gRPC service the control plane dials: it AXFRs the
# declared zones and streams the reach coordinates it finds as ObservedEntities, and
# writes records by RFC 2136 dynamic update so a name declared in Git becomes a fact
# Stratt CAUSED. Apache-2.0 Go; its one third-party dependency (miekg/dns) is
# BSD-3-Clause; no copyleft; imports nothing from core/.
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
COPY plugins/dns/ plugins/dns/
WORKDIR /src/plugins/dns
ENV GOWORK=off CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build go build -trimpath -o /out/stratt-plugin-dns ./cmd/stratt-plugin-dns

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratt-plugin-dns /usr/local/bin/stratt-plugin-dns
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/stratt-plugin-dns"]
