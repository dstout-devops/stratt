# A vspheresim guest — an ordinary Linux host you can SSH into and configure.
#
# vspheresim gives every VM a real container guest, which made a provisioned VM
# REACHABLE: it reports a hostname and an address, so a reachability coordinate
# resolves to something. Reachable is not the same as converge-able. A stock base
# image answers no port, so the coordinate led to a host that could be named and
# never logged into — provision→configure still could not be exercised end to end.
#
# This image closes that. It is the guest OS: sshd for the reach path, and python3
# because that is what a configuration tool needs on the FAR side of it.
#
# EXTRACTION INTENT (see ../main.go). Nothing here knows anything about Stratt —
# the authorized key arrives as a build argument, and who supplies it is the
# harness's business. It travels with the simulator.
FROM public.ecr.aws/docker/library/alpine:3.24

# openssh brings both halves: sshd for the guest, and the client, which lets this
# same image act as the peer that reaches a guest (see the live proof) without
# pulling a second image for the other end of the connection.
RUN apk add --no-cache openssh python3

# The public half of a keypair whose private half is whatever the caller will
# converge with. Empty is allowed and warned about at boot rather than refused:
# a guest with no key is still a legitimate reachability fixture, and a build that
# failed here would look like an image problem instead of a missing argument.
ARG AUTHORIZED_KEY=""
RUN mkdir -p /root/.ssh \
    && printf '%s\n' "$AUTHORIZED_KEY" >/root/.ssh/authorized_keys \
    && chmod 700 /root/.ssh && chmod 600 /root/.ssh/authorized_keys \
    && sed -i 's/^#*PermitRootLogin.*/PermitRootLogin prohibit-password/' /etc/ssh/sshd_config

COPY boot.sh /usr/local/sbin/boot.sh
EXPOSE 22
ENTRYPOINT ["/usr/local/sbin/boot.sh"]
