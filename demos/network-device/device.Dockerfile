# A NETWORK DEVICE, not a Linux host that happens to run a routing daemon.
#
# The difference is the login shell. `netops`'s shell is /usr/bin/vtysh, so an SSH session lands in
# the device's own CLI — there is no POSIX shell behind it, no `python` to run a module on, no
# scp'ing a script into /tmp. That is exactly what a switch presents, and it is why ansible needs a
# CONNECTION PLUGIN (network_cli) rather than its ordinary ssh path: every task has to be expressed
# as CLI commands typed at a prompt and parsed back out of the terminal.
#
# WHY FRR AND NOT cEOS/vJunos. Those need registration and a licence, which makes them unusable as a
# CI gate — the reason ADR-0153's live half sat booked as "blocked on a target". FRR is Apache-2.0,
# ships in Alpine's repository, and speaks a real vtysh CLI. Nothing here is simulated: vtysh is the
# actual FRR shell, the commands are real FRR commands, and the running-config the demo reads back is
# the daemon's own.
FROM alpine:3.21

RUN apk add --no-cache frr frr-pythontools openssh \
    # ssh host keys, generated at BUILD time so the device's identity is stable across restarts —
    # the demo declares hostKeyChecking: accept-new, which accepts a first key and refuses a
    # CHANGED one, and a key regenerated on every boot would make that guarantee meaningless.
 && ssh-keygen -A \
    # THE LOGIN SHELL IS THE CLI. Everything else in this file is ordinary container plumbing;
    # this line is what makes the target a network device.
 && adduser -D -s /usr/bin/vtysh netops \
    # UNLOCK THE ACCOUNT, and this line cost a live run. `adduser -D` creates the user with no
    # password, which on Alpine writes `!` into the /etc/shadow field — and `!` means LOCKED, not
    # "no password". OpenSSH refuses a locked account outright, BEFORE it ever looks at
    # authorized_keys, so public-key auth fails with `Disconnected from invalid user netops` on the
    # device side and `Access denied for 'publickey'` on ansible's — two messages that both point
    # at the key, when the key was always correct.
    #
    # `*` means "no password can ever match" (still no password login, and PasswordAuthentication is
    # off below anyway) WITHOUT marking the account locked. sed rather than `passwd -u` because
    # busybox's passwd refuses to unlock an account that has no password to unlock.
 && sed -i 's/^netops:!/netops:*/' /etc/shadow \
    # vtysh talks to the daemons over sockets in /var/run/frr, readable by the frr groups only.
 && addgroup netops frr \
 && addgroup netops frrvty \
 && mkdir -p /var/run/frr /run/frr /home/netops/.ssh \
 && chown -R frr:frr /var/run/frr /run/frr /etc/frr \
 && chown -R netops:netops /home/netops/.ssh \
 && chmod 700 /home/netops/.ssh \
 && printf 'zebra=yes\nmgmtd=yes\nstaticd=yes\nbgpd=yes\n' > /etc/frr/daemons \
 && printf 'hostname stratt-rtr-01\nlog stdout\n!\n' > /etc/frr/frr.conf \
 && chown frr:frr /etc/frr/frr.conf && chmod 640 /etc/frr/frr.conf \
    # Key auth only: the device credential is a brokered CredentialRef (§2.5), never a password in
    # a manifest.
 && printf 'PasswordAuthentication no\nPermitRootLogin no\nPubkeyAuthentication yes\n' >> /etc/ssh/sshd_config

# mgmtd IS REQUIRED and its absence is a silent failure worth recording. FRR 10.x routes staticd's
# configuration through mgmtd, so without it a `configure terminal` session opens, accepts the
# command, and the DEVICE answers `mgmtd is not running` — the config push fails at the device
# rather than at the connection, which looks like an ansible problem and is not. Measured on
# 2026-08-03: adding mgmtd turned `failed=1` into `changed=1`.
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
EXPOSE 22
CMD ["/usr/local/bin/entrypoint.sh"]
