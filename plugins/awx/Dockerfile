# syntax=docker/dockerfile:1
# stratt-plugin-awx — the AWX/AAP Connector over the sovereign port (ADR-0046/0089). A
# long-lived gRPC service the control plane dials. It BOTH projects the Controller's
# /api/v2 (read-only, §1.2) as `ansible.*` ObservedEntities AND provides the
# adopt/materialize Action — the deep-read + AWX→CaC transform (ADR-0089: the AWX breadth
# lives here, not the spine), resolving the AWX CredentialRef via its own SecretBroker
# in-pod (§2.5). AWX stays authoritative and keeps executing. Apache-2.0 Go; no copyleft;
# imports nothing from core/.
#
# Build context is the repo root — the module + its replace-directive deps (sdk,
# sdk/secretbroker, types).
FROM golang:1.26 AS build
WORKDIR /src
COPY sdk/ sdk/
COPY types/ types/
COPY plugins/awx/ plugins/awx/
WORKDIR /src/plugins/awx
ENV GOWORK=off CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build go build -trimpath -o /out/stratt-plugin-awx ./cmd/stratt-plugin-awx

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratt-plugin-awx /usr/local/bin/stratt-plugin-awx
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/stratt-plugin-awx"]
