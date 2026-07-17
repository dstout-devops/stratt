# stratt-plugin-staticinv — the static-inventory Syncer plugin over the sovereign
# port (ADR-0056 §5). A long-lived gRPC service the control plane dials
# (STRATT_STATICINV_PLUGIN_ADDR); it reads the estate's host-list files and
# streams typed `host` ObservedEntities back, governed hub-side. Apache-2.0 Go;
# no copyleft; imports nothing from core/.
#
# Build context is the repo root — the module + its deps (the lean sdk).
FROM golang:1.26 AS build
WORKDIR /src
COPY sdk/ sdk/
COPY plugins/staticinv/ plugins/staticinv/
WORKDIR /src/plugins/staticinv
ENV GOWORK=off CGO_ENABLED=0
RUN go build -trimpath -o /out/stratt-plugin-staticinv ./cmd/stratt-plugin-staticinv

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratt-plugin-staticinv /usr/local/bin/stratt-plugin-staticinv
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/stratt-plugin-staticinv"]
