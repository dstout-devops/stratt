# stratt-plugin-vcenter — the vCenter Syncer plugin over the sovereign port
# (ADR-0046). A long-lived gRPC service the control plane dials
# (STRATT_VCENTER_PLUGIN_ADDR); it enumerates the vCenter/vcsim inventory with
# govmomi and streams typed ObservedEntities back, governed hub-side. Apache-2.0
# Go; no copyleft.
#
# Build context is the repo root — the module + its deps (the lean sdk).
FROM golang:1.26 AS build
WORKDIR /src
COPY sdk/ sdk/
COPY plugins/vcenter/ plugins/vcenter/
WORKDIR /src/plugins/vcenter
ENV GOWORK=off CGO_ENABLED=0
RUN go build -trimpath -o /out/stratt-plugin-vcenter ./cmd/stratt-plugin-vcenter

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratt-plugin-vcenter /usr/local/bin/stratt-plugin-vcenter
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/stratt-plugin-vcenter"]
