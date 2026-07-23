# syntax=docker/dockerfile:1
# stratt-forwarder image (ADR-0034): ships the one audit stream to a SIEM. A
# single static Go binary on distroless nonroot — no shell, no package manager
# (§7.3). It reads audit batches from the platform API and delivers them through
# a vendor-neutral driver (Splunk HEC / syslog / OTel-logs).
#
# NEVER copy .env/secret files into any stage (§2.5): the SIEM credential is a
# CredentialRef injected as a mounted Secret at pod spawn; the control plane and
# this image hold only pointers.
#
# Build from the repo root:
#   docker build -f deploy/docker/stratt-forwarder.Dockerfile -t stratt/stratt-forwarder:dev .

# GOWORK=off: build from core/go.mod alone; the workspace's sdk/plugins modules
# are NOT in this image's context (the control plane never links them, ADR-0046).
# core's replace directives resolve types/contracts/packs/sdk locally.
FROM golang:1.26 AS build
WORKDIR /src
ENV GOWORK=off
COPY types/ types/
COPY contracts/ contracts/
COPY packs/ packs/
COPY sdk/ sdk/
COPY core/ core/
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build go -C core mod download
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -C core -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" -o /out/stratt-forwarder ./cmd/stratt-forwarder

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratt-forwarder /usr/local/bin/stratt-forwarder
# Numeric uid:gid (distroless nonroot): kubelet must VERIFY runAsNonRoot.
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/stratt-forwarder"]
