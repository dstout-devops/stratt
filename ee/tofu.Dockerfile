# Stratt OpenTofu execution-environment image (charter §3: OpenTofu over
# Terraform; ADR-0016). Runs `tofu` as tool content in ephemeral K8s Job pods
# — same /runner layout and non-root posture as the ansible EE. Python hosts
# the event driver (ADR-0002: Python lives in execution pods only).
#
# Pins ride the quarterly evergreen train (§1.7). The binary is
# checksum-verified against the signed release manifest (§7.3).

FROM python:3.14-alpine

ARG TOFU_VERSION=1.12.3
ARG TARGETARCH=amd64

RUN apk add --no-cache ca-certificates \
    && wget -q "https://github.com/opentofu/opentofu/releases/download/v${TOFU_VERSION}/tofu_${TOFU_VERSION}_linux_${TARGETARCH}.zip" \
    && wget -q "https://github.com/opentofu/opentofu/releases/download/v${TOFU_VERSION}/tofu_${TOFU_VERSION}_SHA256SUMS" \
    && grep "tofu_${TOFU_VERSION}_linux_${TARGETARCH}.zip" "tofu_${TOFU_VERSION}_SHA256SUMS" | sha256sum -c - \
    && unzip -q "tofu_${TOFU_VERSION}_linux_${TARGETARCH}.zip" tofu -d /usr/local/bin \
    && rm -f "tofu_${TOFU_VERSION}_linux_${TARGETARCH}.zip" "tofu_${TOFU_VERSION}_SHA256SUMS" \
    && tofu version

# Non-root runner, uid/gid 1000 — matches the dispatcher's fsGroup contract
# (ADR-0009 correction: credential files project group-readable to gid 1000).
RUN addgroup -g 1000 runner && adduser -D -u 1000 -G runner runner \
    && mkdir -p /runner && chown runner:runner /runner
USER runner
WORKDIR /runner

# The dispatcher supplies the command (the Actuator's driver).
