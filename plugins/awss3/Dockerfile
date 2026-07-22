# stratt-plugin-awss3 — the S3 Connector plugin over the sovereign port (ADR-0046/0097):
# bucket lifecycle Actions + a metadata-only bucket Syncer. The control plane dials it
# (STRATT_AWSS3_PLUGIN_ADDR) and governs what it may write. Apache-2.0 Go; no copyleft;
# imports nothing from core/.
#
# Build context is the repo root — the module + its deps (the lean sdk).
FROM golang:1.26 AS build
WORKDIR /src
COPY sdk/ sdk/
COPY plugins/awss3/ plugins/awss3/
WORKDIR /src/plugins/awss3
ENV GOWORK=off CGO_ENABLED=0
RUN go build -trimpath -o /out/stratt-plugin-awss3 ./cmd/stratt-plugin-awss3

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratt-plugin-awss3 /usr/local/bin/stratt-plugin-awss3
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/stratt-plugin-awss3"]
