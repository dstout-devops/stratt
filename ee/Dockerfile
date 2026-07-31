# syntax=docker/dockerfile:1
# Stratt execution-environment (EE) image — the ansible Actuator's runtime
# (charter §3, ADR-0002). This is the GPLv3 boundary: Ansible lives HERE, in a
# separate image executed as a subprocess in ephemeral K8s Job pods. The Go
# control plane never links it.
#
# ansible-builder EE compatibility is a charter commitment (§3); this dev
# image keeps the same /runner private-data-dir contract ansible-runner
# defines, so ansible-builder-produced EEs are drop-in replacements.
#
# Pins ride the quarterly evergreen train (§1.7).

# ── stratt-ansible shim (ADR-0051): the port-speaking binary baked into the EE ──
# Apache-2.0 Go that reads the sovereign ApplyRequest, shells out to ansible-runner
# (the GPL boundary stays a SUBPROCESS — never linked), and emits the port's typed
# shapes as proto-JSON on stdout. The EE-Job transport runs THIS as the pod command;
# the core forwards its typed stream and governs hub-side (GovernStream). Build
# context is the repo root — the module + its only dep (the lean sdk, via replace).
FROM golang:1.26 AS shim
# Bound compile fan-out (same lever + default as the Taskfile's host gates). Without it a
# cold cache compiles the whole dependency graph at -p=NumCPU, which on a many-core host
# over overlayfs corrupts the build cache it is writing (`internal compiler error: export
# data desync` in a random dependency) and can exhaust host memory outright.
ARG GO_PARALLEL=8
ENV GOFLAGS=-p=${GO_PARALLEL}
WORKDIR /src
COPY sdk/ sdk/
COPY plugins/ansible/ plugins/ansible/
COPY plugins/script/ plugins/script/
ENV GOWORK=off CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build cd plugins/ansible && go build -trimpath -o /out/stratt-ansible ./cmd/stratt-ansible
# stratt-script (ADR-0046 Category A): the per-target script-runner over the port.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build cd plugins/script && go build -trimpath -o /out/stratt-script ./cmd/stratt-script

FROM python:3.14-slim

# git + ssh client: the ansible Actuator's SCM content-ref (charter §5.6,
# ADR-0025) clones a repo in this pod (the GPL boundary keeps git — like
# Ansible — out of the Go control plane). ca-certificates for https clones.
RUN apt-get update \
    && apt-get install -y --no-install-recommends git ca-certificates openssh-client \
    && rm -rf /var/lib/apt/lists/*

# uv is the only sanctioned Python package manager (.claude/rules/backend-python.md).
COPY --from=ghcr.io/astral-sh/uv:0.9 /uv /usr/local/bin/uv

RUN uv pip install --system --no-cache \
    "ansible-core>=2.19,<2.20" \
    "ansible-runner>=2.4,<3"

# ── Ansible content: collections + roles (ADR-0117 D3) ────────────────────────
# Content is declared where it is RESOLVED — HERE, in the build — never as a field on
# the run-time ansible.input Contract. A Step selects content by selecting an Actuator
# whose declaration carries this image (D3a), so the image is the content boundary.
# (Digest-PINNING an image reference in production is the operator's job under §7.3; this
# dev tag is not a digest.) Build-time because a run
# pod fetching content from the network has no provenance (§7.3), because Galaxy/private
# Hub is an EXTERNAL system that must not be in the critical path of every converge
# (PLG-1), and because air-gap is then a seeding problem rather than a runtime one.
#
# EE_CONTENT names a Galaxy requirements file under ee/content/ (the real format, which
# ansible-builder also consumes — §3 compatibility). It DEFAULTS to the platform floor
# (platform.requirements.yml) rather than to the empty string, and that is the ANS-012 fix:
# an empty default meant the platform's own content set was an unreviewable build arg nobody
# had chosen, and `ansible.builtin.package` — a dispatcher, not an implementation — could not
# install anything on a target whose package manager lives outside ansible-core. Empty is still
# expressible (`--build-arg EE_CONTENT=`) and still writes a manifest, so a contentless image
# remains available as a DELIBERATE choice rather than as what you get by not deciding.
#
# Exactly one file per image: a lockfile records the installed CLOSURE, which cannot be
# attributed to one of several declarations. So a variant REPEATS the floor instead of
# composing it, and `ee/content.py verify` fails `task ci` if a variant drops or re-pins a
# floor entry — a variant adds capability and never subtracts it.
#
# Installs into /usr/share/ansible/{collections,roles}, which is on ansible's DEFAULT
# search path — verified live: ansible-runner adds its own project dirs WITHOUT replacing
# the defaults, so no ANSIBLE_ROLES_PATH / ANSIBLE_COLLECTIONS_PATH / ansible.cfg is
# needed, and an INLINE play can use a build-time role. content.py asserts this at build
# time rather than trusting it, since ansible-runner floats within its version band.
#
# content.py enforces exact pins BEFORE touching the network and then verifies the
# RESOLVED set against the declaration (verify-don't-trust): an image whose contents
# differ from what it declares makes its digest an unreliable claim about the Run.
#
# It then verifies each installed artifact's SHA-256 against the in-repo LOCKFILE beside
# the declaration (ADR-0117 follow-up i). The pin rule bounds which VERSION; only the
# lockfile bounds which BYTES — without it the registry was the sole checksum authority, so
# a republished artifact at the same version number produced a different image from an
# unchanged declaration and nothing said so. Build FAILS on any mismatch, and on an
# artifact (including a new transitive dependency) that no lockfile entry covers.
# EE_LOCK_MODE=write regenerates the lockfile instead of checking it, and is for
# `task ee:content:lock` alone — a build that writes its own expectations proves nothing.
#
# Note the deliberate asymmetry with the ansible-core/ansible-runner ranges above: the
# TOOLCHAIN floats within a supported band because evergreen (§1.7) wants it current and
# CI-gated at N-1, whereas CONTENT is pinned exactly because reproducibility of what a Run
# executed is the property D3 buys. Different properties, different rules — not an
# oversight.
ARG EE_CONTENT="platform.requirements.yml"
ARG EE_LOCK_MODE="verify"
# EE_OFFLINE (ADR-0124 D2): a subdirectory of ee/content/ holding pre-downloaded collection
# tarballs. Set it and the install reaches NO registry — the pin check and the lockfile check are
# unchanged, so an air-gapped build is verified by the same hash a connected one is, not trusted
# because of where the bytes came from. Populate it with `task ee:content:pull` on a connected
# machine. Empty (the default) installs from Galaxy exactly as before.
ARG EE_OFFLINE=""
COPY ee/content.py /usr/local/bin/stratt-ee-content
COPY ee/content/ /usr/share/stratt/ee-content/
RUN python /usr/local/bin/stratt-ee-content install \
      --lock-mode="${EE_LOCK_MODE}" \
      ${EE_OFFLINE:+--artifacts=/usr/share/stratt/ee-content/${EE_OFFLINE}} \
      ${EE_CONTENT:+/usr/share/stratt/ee-content/${EE_CONTENT}} \
    && chmod -R a+rX /usr/share/ansible

# ── Control-node BINARIES some connection plugins exec (ADR-0156 D6) ──────────────────────
# A collection is not always enough. kubernetes.core.kubectl SHELLS OUT to `kubectl`, and
# amazon.aws.aws_ssm to `session-manager-plugin` — so an image can carry the collection and
# still be unable to make the connection. The shim checks both axes before a play runs; this
# is where the second axis gets satisfied.
#
# DECLARED PER IMAGE, exactly as content is: empty by default, so the base EE stays lean and a
# variant that needs the binary asks for it by version. Nobody gets 50MB of kubectl by not
# deciding — the same rule ANS-012 set when an empty EE_CONTENT turned out to be a build-arg
# default rather than a choice.
#
# PINNED BY DIGEST, not just by version, and verified before it is trusted. A version bounds
# WHICH release; only the checksum bounds WHICH BYTES — the identical argument ee/content.py's
# lockfile makes for collections, applied to a binary downloaded over the network at build time.
ARG EE_KUBECTL_VERSION=""
ARG EE_KUBECTL_SHA256=""
RUN if [ -n "${EE_KUBECTL_VERSION}" ]; then       set -eu;       arch="$(dpkg --print-architecture)";       url="https://dl.k8s.io/release/v${EE_KUBECTL_VERSION}/bin/linux/${arch}/kubectl";       echo "stratt-ee: fetching kubectl ${EE_KUBECTL_VERSION} (${arch})";       python -c "import urllib.request,sys; urllib.request.urlretrieve(sys.argv[1], '/usr/local/bin/kubectl')" "$url";       if [ -z "${EE_KUBECTL_SHA256}" ]; then         echo "stratt-ee: EE_KUBECTL_VERSION is set but EE_KUBECTL_SHA256 is not — a version bounds which";         echo "           RELEASE, only the checksum bounds which BYTES, and an unverified binary makes the";         echo "           image digest an unreliable claim about what a Run executed (ADR-0117 D3's argument)";         exit 1;       fi;       echo "${EE_KUBECTL_SHA256}  /usr/local/bin/kubectl" | sha256sum -c -;       chmod 0755 /usr/local/bin/kubectl;       kubectl version --client=true --output=yaml | head -3;     fi

# The port-speaking shims (ADR-0051/0046): the EE-Job transport's pod commands. Each
# reads /runner/stratt/request.json (the sovereign ApplyRequest) and emits typed
# ApplyResponses. stratt-ansible runs ansible-runner; stratt-script runs user scripts.
COPY --from=shim /out/stratt-ansible /usr/local/bin/stratt-ansible
COPY --from=shim /out/stratt-script /usr/local/bin/stratt-script

# Non-root: the dispatcher's pods run unprivileged.
RUN useradd --create-home --uid 1000 runner \
    && mkdir -p /runner/project /runner/inventory /runner/artifacts /runner/stratt \
    && chown -R runner:runner /runner
USER runner
WORKDIR /runner

# The dispatcher overrides the command: the EE-Job transport (ADR-0051) sets
# ["stratt-ansible"]; this default documents the legacy in-tree-actuator contract.
CMD ["ansible-runner", "run", "/runner", "-p", "play.yml", "-j"]
