# stratt-plugin-helm — the Helm Actuator over the sovereign port (ADR-0092). A
# long-lived gRPC service the control plane dials as a Blueprint route's `app:`
# materializer: Plan renders (helm template), Apply converges (helm upgrade
# --install). Apache-2.0 Go; no copyleft; imports nothing from core/.
#
# Build context is the repo root — the module + its deps (the lean sdk + yaml.v3).
FROM golang:1.26 AS build
WORKDIR /src
COPY sdk/ sdk/
COPY plugins/helm/ plugins/helm/
WORKDIR /src/plugins/helm
ENV GOWORK=off CGO_ENABLED=0
RUN go build -trimpath -o /out/stratt-plugin-helm ./cmd/stratt-plugin-helm

# Fetch + VERIFY the pinned Helm 4 binary (§7.3 supply chain; dependency-scout
# binding condition — checksum-verified, not a bare download). helm ships as a
# static Go binary, so it runs in distroless static. Pin BOTH version and sha256;
# bump on the quarterly upgrade train (N-1 v3.21.3 kept working — ADR-0092).
ARG HELM_VERSION=v4.2.3
ARG HELM_SHA256=e9b88b4ee95b18c706839c28d3a0220e5bc470e9cd9262410c90793c45ff8b7c
RUN set -eux; \
    curl -fsSL -o /tmp/helm.tgz "https://get.helm.sh/helm-${HELM_VERSION}-linux-amd64.tar.gz"; \
    echo "${HELM_SHA256}  /tmp/helm.tgz" | sha256sum -c -; \
    tar -xzf /tmp/helm.tgz -C /tmp linux-amd64/helm; \
    install -m 0755 /tmp/linux-amd64/helm /out/helm

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratt-plugin-helm /usr/local/bin/stratt-plugin-helm
COPY --from=build /out/helm /usr/local/bin/helm
# helm caches/config live under a writable dir (the pod rootfs is read-only, §7.3);
# the chart mounts an emptyDir at /tmp and the plugin points HELM_*_HOME at /tmp/helm.
ENV STRATT_HELM_BIN=/usr/local/bin/helm
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/stratt-plugin-helm"]
