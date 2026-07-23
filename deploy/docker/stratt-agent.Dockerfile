# syntax=docker/dockerfile:1
# stratt-agent image (ADR-0032): the Site satellite dispatcher. A single static
# Go binary on distroless nonroot — it needs no shell and no python (tool content
# runs in the EE pods it spawns, not here). §1.4 boring-spine: same build shape
# as strattd, same distroless posture (§7.3).
#
# NEVER copy .env/secret files into any stage (§2.5): the agent resolves
# credential pointers against Secrets in its OWN cluster at pod spawn.
#
# Build from the repo root:
#   docker build -f deploy/docker/stratt-agent.Dockerfile -t stratt/stratt-agent:dev .

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
    -ldflags "-s -w -X main.version=${VERSION}" -o /out/stratt-agent ./cmd/stratt-agent

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratt-agent /usr/local/bin/stratt-agent
# Numeric uid:gid (distroless nonroot): kubelet must VERIFY runAsNonRoot.
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/stratt-agent"]
