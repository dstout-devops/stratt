# syntax=docker/dockerfile:1
# stratt-plugin-openbao — the multi-capability OpenBao provider (ADR-0106) over the sovereign
# port: the `certissuer` capability (PKI mint/renew) and the KV surfaces, dialed by the control
# plane. Capabilities are named by CLASS in an estate, never by this provider name, which is what
# lets a step-ca implementation replace it without an estate edit (§1.5).
#
# It authenticates to OpenBao with a brokered CredentialRef injected at pod spawn — no token is
# baked into a layer (§2.5). Apache-2.0 Go.
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
COPY plugins/openbao/ plugins/openbao/
WORKDIR /src/plugins/openbao
ENV GOWORK=off CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build go build -trimpath -o /out/stratt-plugin-openbao ./cmd/stratt-plugin-openbao

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratt-plugin-openbao /usr/local/bin/stratt-plugin-openbao
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/stratt-plugin-openbao"]
