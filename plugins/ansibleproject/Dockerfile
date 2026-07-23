# syntax=docker/dockerfile:1
# stratt-plugin-ansibleproject — the ansible-project Connector over the sovereign port
# (ADR-0046). A long-lived gRPC service the control plane dials; it reads a raw Ansible
# content root (STRATT_ANSIBLE_PROJECT_ROOT — a mounted Git checkout / directory of
# playbooks, roles, requirements.yml, inventory) READ-ONLY (§1.2, §2.5) and projects its
# artifacts — playbooks, roles, collections, inventories — as `ansible.*` ObservedEntities.
# Git stays authoritative; the plugin holds no write credential to the root. This is
# "Ansible without AWX", the primitive half of the `ansible` domain. Apache-2.0 Go; no
# copyleft; imports nothing from core/.
#
# Build context is the repo root — the module + its deps (the lean sdk, yaml).
FROM golang:1.26 AS build
WORKDIR /src
COPY sdk/ sdk/
COPY plugins/ansibleproject/ plugins/ansibleproject/
WORKDIR /src/plugins/ansibleproject
ENV GOWORK=off CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build go build -trimpath -o /out/stratt-plugin-ansibleproject ./cmd/stratt-plugin-ansibleproject

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratt-plugin-ansibleproject /usr/local/bin/stratt-plugin-ansibleproject
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/stratt-plugin-ansibleproject"]
