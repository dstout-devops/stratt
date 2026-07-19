# stratt-plugin-awx — the AWX/AAP Connector over the sovereign port (ADR-0046). A
# long-lived gRPC service the control plane dials; it reads an AWX Controller's /api/v2
# (read-only, §1.2) and projects its automation estate — job templates, workflows,
# schedules, orgs, teams — as `ansible.*` ObservedEntities. AWX stays authoritative and
# keeps executing; this is the always-on mirror. Apache-2.0 Go; no copyleft; imports
# nothing from core/.
#
# Build context is the repo root — the module + its deps (the lean sdk).
FROM golang:1.26 AS build
WORKDIR /src
COPY sdk/ sdk/
COPY plugins/awx/ plugins/awx/
WORKDIR /src/plugins/awx
ENV GOWORK=off CGO_ENABLED=0
RUN go build -trimpath -o /out/stratt-plugin-awx ./cmd/stratt-plugin-awx

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratt-plugin-awx /usr/local/bin/stratt-plugin-awx
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/stratt-plugin-awx"]
