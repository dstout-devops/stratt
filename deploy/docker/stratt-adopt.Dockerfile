# stratt-adopt — the adopt/materialize Action server over the sovereign plugin port
# (ADR-0088). A long-lived gRPC service the control plane dials (STRATT_ADOPT_PLUGIN_ADDR).
# Unlike the SDK-only dark-matter plugins, this is CORE-OWNED: its Invoke runs the core
# awximport transform, so it builds from core/go.mod. It resolves the AWX CredentialRef via
# the SecretBroker (in-cluster K8s Secret reads under its OWN confined RBAC — the notify MF-A
# pattern) and does the targeted deep-read + transform in-pod. AWX material never crosses the
# core (§2.5). Apache-2.0 Go; no copyleft (never links Ansible).
#
# Build from the repo root:
#   docker build -f deploy/docker/stratt-adopt.Dockerfile -t stratt/stratt-adopt:dev .

# ── control plane (core module) ──────────────────────────────────────────────
# GOWORK=off: build from core/go.mod alone. core's replace directives resolve
# types/contracts/packs/sdk/sdk-secretbroker locally, so copy exactly those.
FROM golang:1.26 AS build
WORKDIR /src
ENV GOWORK=off
COPY types/ types/
COPY contracts/ contracts/
COPY packs/ packs/
COPY sdk/ sdk/
COPY core/ core/
RUN go -C core mod download
RUN CGO_ENABLED=0 go build -C core -trimpath -ldflags "-s -w" -o /out/stratt-adopt ./cmd/stratt-adopt

# ── runtime ──────────────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratt-adopt /usr/local/bin/stratt-adopt
# Numeric uid:gid (distroless nonroot): kubelet must be able to VERIFY runAsNonRoot.
USER 65532:65532
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/stratt-adopt"]
