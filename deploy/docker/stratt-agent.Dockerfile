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

FROM golang:1.26 AS build
WORKDIR /src
COPY go.work go.work.sum ./
COPY types/ types/
COPY core/ core/
RUN go mod download
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -C core -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" -o /out/stratt-agent ./cmd/stratt-agent

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratt-agent /usr/local/bin/stratt-agent
# Numeric uid:gid (distroless nonroot): kubelet must VERIFY runAsNonRoot.
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/stratt-agent"]
