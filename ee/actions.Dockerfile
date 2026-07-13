# Stratt Actions execution-environment image (ADR-0031). Carries the Go
# drivers for Connector Actions that call vendor SDKs directly — currently
# awsec2/create-vm (cmd/actions-ec2), reusing the vendored aws-sdk-go-v2 (§1.4
# — no boto3). Same /runner layout + non-root uid/gid 1000 as the other EE
# images (matches the dispatcher's fsGroup contract, ADR-0009). Credentials
# arrive from injected env/Secrets at runtime only — never COPYed in (§2.5).
#
# Build from the repo root:
#   docker build -f ee/actions.Dockerfile -t stratt-ee-actions:dev .

FROM golang:1.26 AS build
WORKDIR /src
COPY go.work go.work.sum ./
COPY types/ types/
COPY contracts/ contracts/
COPY core/ core/
RUN go mod download
RUN CGO_ENABLED=0 go build -C core -trimpath -ldflags "-s -w" -o /out/actions-ec2 ./cmd/actions-ec2

FROM alpine:3.21
RUN apk add --no-cache ca-certificates \
    && addgroup -g 1000 runner && adduser -D -u 1000 -G runner runner \
    && mkdir -p /runner && chown runner:runner /runner
COPY --from=build /out/actions-ec2 /actions-ec2
USER runner
WORKDIR /runner
# The dispatcher supplies the command (the Action's driver).
