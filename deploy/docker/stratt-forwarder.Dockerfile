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

FROM golang:1.26 AS build
WORKDIR /src
COPY go.work go.work.sum ./
COPY types/ types/
COPY contracts/ contracts/
COPY packs/ packs/
COPY core/ core/
RUN go mod download
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -C core -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" -o /out/stratt-forwarder ./cmd/stratt-forwarder

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratt-forwarder /usr/local/bin/stratt-forwarder
# Numeric uid:gid (distroless nonroot): kubelet must VERIFY runAsNonRoot.
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/stratt-forwarder"]
