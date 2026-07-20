# stratt-plugin-netbox — the NetBox Syncer plugin over the sovereign port
# (ADR-0046/0059). A long-lived gRPC service the control plane dials
# (STRATT_NETBOX_PLUGIN_ADDR); it reads NetBox's IPAM/DCIM REST API and streams
# typed `subnet`/`vlan` ObservedEntities (+ placement Relations) back, governed
# hub-side. Apache-2.0 Go; no copyleft; imports nothing from core/.
#
# Build context is the repo root — the module + its deps (the lean sdk).
FROM golang:1.26 AS build
WORKDIR /src
COPY sdk/ sdk/
COPY plugins/netbox/ plugins/netbox/
WORKDIR /src/plugins/netbox
ENV GOWORK=off CGO_ENABLED=0
RUN go build -trimpath -o /out/stratt-plugin-netbox ./cmd/stratt-plugin-netbox

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratt-plugin-netbox /usr/local/bin/stratt-plugin-netbox
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/stratt-plugin-netbox"]
