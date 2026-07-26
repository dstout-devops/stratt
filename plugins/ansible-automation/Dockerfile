# syntax=docker/dockerfile:1
# stratt-plugin-ansible-automation — the Ansible Automation Platform Connector over the
# sovereign port (ADR-0046/0089/0127). A long-lived gRPC service the control plane dials.
# ONE image serving TWO halves, selected per instance by STRATT_ANSIBLE_AUTOMATION_ROLE:
#
#   role=controller — projects an AAP Controller's /api/v2 (read-only, §1.2) as
#                     ansible.template/.workflow/.schedule/.org/.team, AND provides the
#                     adopt/materialize Action — the deep-read + AWX→CaC transform
#                     (ADR-0089: the breadth lives here, not the spine), resolving the
#                     Controller CredentialRef via its own SecretBroker in-pod (§2.5).
#   role=content    — projects a mounted Ansible content root as
#                     ansible.playbook/.role/.collection/.inventory. No credential, no
#                     network egress, no Secret access (the role selector is the §2.5
#                     least-privilege boundary — a content instance builds no broker).
#
# Each half registers its own Grant and therefore its own graph.source, so ONE INSTANCE PER
# SOURCE (ADR-0127 D1): two Controllers is two Deployments off this one image, exactly as
# two vCenters is two vcenter Deployments. AAP and Git stay authoritative and AAP keeps
# executing. Apache-2.0 Go; no copyleft; imports nothing from core/.
#
# Build context is the repo root — the module + its replace-directive deps (sdk,
# sdk/secretbroker, types).
FROM golang:1.26 AS build
# Bound compile fan-out (same lever + default as the Taskfile's host gates). Without it a
# cold cache compiles the whole dependency graph at -p=NumCPU, which on a many-core host
# over overlayfs corrupts the build cache it is writing (`internal compiler error: export
# data desync` in a random dependency) and can exhaust host memory outright.
ARG GO_PARALLEL=8
ENV GOFLAGS=-p=${GO_PARALLEL}
WORKDIR /src
COPY sdk/ sdk/
COPY types/ types/
COPY plugins/ansible-automation/ plugins/ansible-automation/
WORKDIR /src/plugins/ansible-automation
ENV GOWORK=off CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build go build -trimpath -o /out/stratt-plugin-ansible-automation ./cmd/stratt-plugin-ansible-automation

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stratt-plugin-ansible-automation /usr/local/bin/stratt-plugin-ansible-automation
USER nonroot:nonroot
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/stratt-plugin-ansible-automation"]
