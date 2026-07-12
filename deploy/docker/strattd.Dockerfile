# strattd control-plane image (ADR-0013). Multi-stage: the UI builds in a
# Node stage, the Go binary builds static, and the runtime is distroless —
# no shell, no package manager, non-root (§7.3 supply-chain posture).
#
# The UI ships as files served via STRATT_UI_DIR (same operational result as
# go:embed with zero code — ADR-0012 deviation recorded in ADR-0013).
#
# NEVER copy .env/secret files into any stage (§2.5): configuration and
# credentials arrive from the environment / mounted Secrets at runtime only.
#
# Build from the repo root:
#   docker build -f deploy/docker/strattd.Dockerfile -t stratt/strattd:dev .

# ── UI ───────────────────────────────────────────────────────────────────────
FROM node:24-slim AS ui
WORKDIR /src/ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci
COPY ui/ ./
COPY core/api/openapi.yaml /src/core/api/openapi.yaml
# Vite bakes env at build time, so the OIDC issuer/client id arrive as build
# args (empty ⇒ the sign-in surface stays hidden; API auth is unaffected —
# the server verifies Bearers regardless of how the SPA was built).
ARG VITE_OIDC_ISSUER=""
ARG VITE_OIDC_CLIENT_ID=""
RUN npm run build

# ── control plane ────────────────────────────────────────────────────────────
FROM golang:1.26 AS build
WORKDIR /src
COPY go.work go.work.sum ./
COPY types/ types/
COPY core/ core/
RUN go mod download
RUN CGO_ENABLED=0 go build -C core -trimpath -ldflags "-s -w" -o /out/strattd ./cmd/strattd

# ── runtime ──────────────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/strattd /usr/local/bin/strattd
COPY --from=ui /src/ui/dist /ui
ENV STRATT_UI_DIR=/ui
# Numeric uid:gid (distroless nonroot): kubelet must be able to VERIFY
# runAsNonRoot, and it can't with a symbolic user (found by the kind e2e).
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/strattd"]
