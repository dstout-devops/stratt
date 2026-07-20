# stratt-plugin-declared — the declared-estate Syncer plugin over the sovereign
# port (ADR-0056 §5). A long-lived gRPC service the control plane dials
# (STRATT_DECLARED_PLUGIN_ADDR); it reads the estate's host-list files and
# streams typed `host` ObservedEntities back, governed hub-side. Apache-2.0 Go;
# no copyleft; imports nothing from core/.
#
# Build context is the repo root — the module + its deps (the lean sdk).
FROM golang:1.26 AS build
WORKDIR /src
COPY sdk/ sdk/
COPY plugins/declared/ plugins/declared/
WORKDIR /src/plugins/declared
ENV GOWORK=off CGO_ENABLED=0
RUN go build -trimpath -o /out/stratt-plugin-declared ./cmd/stratt-plugin-declared

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratt-plugin-declared /usr/local/bin/stratt-plugin-declared
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/stratt-plugin-declared"]
