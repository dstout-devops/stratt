#!/bin/sh
# Bring the FRR daemons up in dependency order, then hand the container to sshd.
#
# ORDER MATTERS and each wait is here because skipping it produced a real failure:
#   * zebra first — it owns the RIB every other daemon registers with;
#   * mgmtd next — FRR 10.x routes staticd's configuration through it, and a config push against a
#     device without it is REJECTED BY THE DEVICE with `mgmtd is not running`, which reads like an
#     ansible fault and is not;
#   * staticd/bgpd last — they connect to both of the above.
set -eu

# The authorized key arrives as a projected Secret (§2.5: brokered, never baked into the image), so
# it is copied to the mode sshd demands. A world-readable authorized_keys is silently ignored.
if [ -f /etc/device-keys/authorized_keys ]; then
    cp /etc/device-keys/authorized_keys /home/netops/.ssh/authorized_keys
    chown netops:netops /home/netops/.ssh/authorized_keys
    chmod 600 /home/netops/.ssh/authorized_keys
fi

# STDERR IS NOT SWALLOWED, and the first version of this file swallowed it. Every daemon here was
# started with `2>/dev/null || echo "did not start (continuing)"`, so when zebra, mgmtd and bgpd all
# failed on a missing capability the log said only "did not start" three times — and the real
# message (`privs_init: initial cap_set_proc failed … Wanted caps: … cap_sys_admin`) went nowhere.
# Two runs were spent rediscovering it. A fixture that hides its own diagnosis is the §1.8 failure
# this repo keeps finding elsewhere, committed here by hand.
start() {
    echo "device: starting $1"
    if ! "/usr/lib/frr/$1" -d; then
        echo "device: ERROR $1 FAILED TO START — see the capability message above; the CLI may still" >&2
        echo "device:       answer while this daemon is dead, which makes the pod look healthy" >&2
    fi
}

# zebra reads the base config; the rest take theirs from mgmtd at runtime.
/usr/lib/frr/zebra -d -f /etc/frr/frr.conf || echo "device: ERROR zebra FAILED TO START" >&2
sleep 1
start mgmtd
sleep 1
start staticd
start bgpd
sleep 1

# Prove the CLI is actually answering before accepting connections. Without this the pod can report
# Ready while vtysh still returns "failed to connect to any daemons", and the first Run of the demo
# fails for a reason that has nothing to do with what it is testing.
if vtysh -c "show version" >/dev/null 2>&1; then
    echo "device: vtysh is answering — the CLI is up"
else
    echo "device: WARNING vtysh cannot reach the daemons yet"
fi

echo "device: sshd listening; netops' login shell is vtysh"
exec /usr/sbin/sshd -D -e
