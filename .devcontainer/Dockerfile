# syntax=docker/dockerfile:1

ARG BASE=debian
ARG VARIANT=trixie
ARG PYTHON_VERSION=3.14

FROM mcr.microsoft.com/devcontainers/python:${PYTHON_VERSION}-${VARIANT} AS final

ENV DEBIAN_FRONTEND=noninteractive
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked,id=apt-cache \
    --mount=type=cache,target=/var/lib/apt/lists,sharing=locked,id=apt-lists-cache \
    set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        curl \
        wget \
        jq \
        postgresql-client; \
    rm -rf /usr/share/doc /usr/share/man;