#!/bin/sh
# First boot of a guest, then sshd in the foreground.
#
# sshd must be exec'd as PID 1 and never exit: vspheresim reads the guest's address
# off a RUNNING container, so a guest whose process ends leaves a VM with a backing
# and no coordinate — which reads exactly like a client that failed to observe.
set -eu

# Host keys at FIRST BOOT, not at build time. Baking them in would give every VM in
# an estate the same host identity, so host-key verification could never distinguish
# two machines and anything that pins one would be silently meaningless. Real
# machines generate theirs on first boot; so does this.
ssh-keygen -A >/dev/null

# A key may also arrive at run time, which is how a guest gets a credential the image
# was not built for. Appended, never replacing: the built-in key is what the harness
# that started this simulator expects to work.
if [ -n "${AUTHORIZED_KEY:-}" ]; then
    printf '%s\n' "$AUTHORIZED_KEY" >>/root/.ssh/authorized_keys
fi

# A guest nobody can log into is the failure this image exists to prevent, and it is
# invisible from the outside — the port answers, the banner is real, and every
# authentication fails. Say so here, where the cause is known.
if ! grep -q '[^[:space:]]' /root/.ssh/authorized_keys; then
    echo "vspheresim-guest: WARNING no authorized key — this guest is reachable but can never be logged into" >&2
fi

# Arguments are REPORTED AND IGNORED, never exec'd in place of sshd.
#
# The usual entrypoint idiom (`[ $# -gt 0 ] && exec "$@"`) would be actively harmful
# here, because vspheresim appends its -guest-args to every guest and that flag
# defaults to `sleep infinity` — a sensible default for a bare base image and a
# silent disabling of sshd for this one. The guest would boot, report an address,
# answer nothing, and look identical to a network fault. sshd is what this image IS;
# a caller wanting some other command wants `docker run --entrypoint`.
if [ "$#" -gt 0 ]; then
    echo "vspheresim-guest: ignoring arguments [$*] — this image's command is sshd (use --entrypoint to run something else)" >&2
fi

exec /usr/sbin/sshd -D -e
