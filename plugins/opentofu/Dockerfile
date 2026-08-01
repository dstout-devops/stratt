# syntax=docker/dockerfile:1
# stratt-plugin-opentofu — the OpenTofu builder/statestore provider (ADR-0112/0145) over the
# sovereign port: it applies the pinned modules an Intent/Subnet build resolves to, against a
# declared S3-compatible backend, and projects the resulting estate back through `stratt_entities`.
#
# The `tofu` BINARY is pinned in the image rather than fetched at run time, for the reason §7.3
# wants of any executable content: a build that downloaded its own tool would make the image digest
# a lie about what a Run executed. Apache-2.0 Go; OpenTofu is MPL-2.0 and runs as a SUBPROCESS,
# never linked.
#
# Build context is the repo root — the module + its deps (the lean sdk).
FROM golang:1.26 AS build
# Bound compile fan-out (same lever + default as the Taskfile's host gates). Without it a
# cold cache compiles the whole dependency graph at -p=NumCPU, which on a many-core host
# over overlayfs corrupts the build cache it is writing and can exhaust host memory.
ARG GO_PARALLEL=8
ENV GOFLAGS=-p=${GO_PARALLEL}
WORKDIR /src
COPY sdk/ sdk/
COPY plugins/opentofu/ plugins/opentofu/
WORKDIR /src/plugins/opentofu
ENV GOWORK=off CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build go build -trimpath -o /out/stratt-plugin-opentofu ./cmd/stratt-plugin-opentofu

# The pinned tofu binary, verified against the SAME sha256 the Taskfile's host gate uses
# (TOFU_VER / TOFU_SHA256_*). Two builds of this image therefore ship byte-identical tooling, and a
# republished release is caught here rather than silently changing what a Run applied — the §7.3
# property the EE content lockfile buys for Ansible content (ADR-0117 i), applied to the one
# executable this plugin shells out to. Keep these ARGs in step with the Taskfile: `task evergreen`
# gates the host copy, and a drift between the two would mean a Run applies with a different tofu
# than a developer validated with.
FROM debian:12-slim AS tofu
ARG TOFU_VER=1.12.5
ARG TOFU_SHA256_AMD64=dade9650e6b74fc7a8b986bd8717497d32f9e09cf82e479afef4977fa3085536
ARG TOFU_SHA256_ARM64=528f4eea63452bbddb30fa4f1780b57fac8d7676f9dda0f772e847bb62c1260a
ARG TARGETARCH
RUN set -eu; \
    apt-get update -qq; \
    apt-get install -y -qq --no-install-recommends ca-certificates curl unzip >/dev/null; \
    case "${TARGETARCH}" in \
      amd64) sha="${TOFU_SHA256_AMD64}" ;; \
      arm64) sha="${TOFU_SHA256_ARM64}" ;; \
      *) echo "unsupported arch: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/tofu.zip \
      "https://github.com/opentofu/opentofu/releases/download/v${TOFU_VER}/tofu_${TOFU_VER}_linux_${TARGETARCH}.zip"; \
    echo "${sha}  /tmp/tofu.zip" | sha256sum -c -; \
    unzip -q -o -d /tmp /tmp/tofu.zip; \
    install -m 0755 /tmp/tofu /out-tofu

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratt-plugin-opentofu /usr/local/bin/stratt-plugin-opentofu
# THE MODULES ARE BAKED, and that is the module-delivery answer ADR-0112 flagged as having no story.
# The plugin resolves `module: aws-network` under STRATT_TOFU_MODULE_ROOT, so without this the pod
# has a tofu binary, a state backend, an allocated CIDR — and nothing to apply.
#
# In the image rather than mounted, for the reason the EE bakes its Ansible content (ADR-0117 D3):
# the image digest is then the whole truth about what a Run applied. A mounted module could change
# under a running floor and the digest would not move. The committed .terraform.lock.hcl beside each
# module is the §7.3 provider pin, so the module tree and its providers are pinned by the same act.
COPY deploy/tofu-modules/ /modules/
COPY --from=tofu /out-tofu /usr/local/bin/tofu
# tofu writes a per-workspace data dir and a shared provider cache under this root. The pod's
# rootfs is read-only, so it must be a writable mount — the chart's `tmpDir: true` supplies one,
# the same lever the helm plugin already uses for its cache.
# The variable name is STRATT_TOFU_DATA_ROOT, matching cmd/stratt-plugin-opentofu. An earlier
# revision set STRATT_OPENTOFU_DATA_ROOT — a plausible-looking name the plugin never reads — so the
# default /var/lib/stratt-tofu stayed in force and the first real build died on
# `mkdir /var/lib/stratt-tofu: read-only file system`. An env var nothing reads is silent until the
# code path that needs it runs.
ENV STRATT_TOFU_DATA_ROOT=/tmp/tofu
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/stratt-plugin-opentofu"]
