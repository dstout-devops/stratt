# syntax=docker/dockerfile:1
# vspheresim — a vCenter SOAP API whose VMs have REAL container guests, packaged so
# it can stand in for the stock `vmware/vcsim` image in a compose substrate.
#
# Build context is the repo root, matching the plugin image beside it. Only sdk/ and
# plugins/vcenter/ are copied: the binary itself imports govmomi and the standard
# library and nothing else, but the module it lives in requires the sdk, so the
# replace directive has to resolve.
FROM golang:1.26 AS build
# Bound compile fan-out — same lever and default as the other plugin images. Without
# it a cold cache compiles the whole dependency graph at -p=NumCPU, which on a
# many-core host over overlayfs corrupts the build cache it is writing.
ARG GO_PARALLEL=8
ENV GOFLAGS=-p=${GO_PARALLEL}
WORKDIR /src
COPY sdk/ sdk/
COPY plugins/vcenter/ plugins/vcenter/
WORKDIR /src/plugins/vcenter
ENV GOWORK=off CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -o /out/vspheresim ./cmd/vspheresim

# NOT distroless, unlike every other image in this repo, and the reason is structural
# rather than an oversight: govmomi's container backing SHELLS OUT — it builds a
# `docker create/run/inspect` command line and hands the joined string to `bash -c`.
# So the runtime needs a docker client and a shell, and a scratch image would leave
# every VM without a guest while the API served perfectly.
#
# It reaches the daemon over a mounted socket, exactly as floci does for the same
# reason: its guests are sibling containers on the host.
FROM public.ecr.aws/docker/library/alpine:3.24
RUN apk add --no-cache docker-cli bash
COPY --from=build /out/vspheresim /usr/local/bin/vspheresim
EXPOSE 8989
ENTRYPOINT ["/usr/local/bin/vspheresim"]
