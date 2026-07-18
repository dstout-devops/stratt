# stratt-plugin-crossplane — the Crossplane build Actuator over the sovereign port
# (ADR-0046/0059). A long-lived gRPC service the control plane dials as the
# `builder:` for network Intents; it applies Crossplane Claims via the K8s API and
# projects the provisioned resource back, governed hub-side. Apache-2.0 Go; no
# copyleft; imports nothing from core/.
#
# Build context is the repo root — the module + its deps (the lean sdk + client-go).
FROM golang:1.26 AS build
WORKDIR /src
COPY sdk/ sdk/
COPY plugins/crossplane/ plugins/crossplane/
WORKDIR /src/plugins/crossplane
ENV GOWORK=off CGO_ENABLED=0
RUN go build -trimpath -o /out/stratt-plugin-crossplane ./cmd/stratt-plugin-crossplane

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratt-plugin-crossplane /usr/local/bin/stratt-plugin-crossplane
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/stratt-plugin-crossplane"]
