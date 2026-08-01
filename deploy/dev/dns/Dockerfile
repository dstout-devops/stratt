# The dev estate's authoritative DNS server — REAL BIND 9, not a simulator.
#
# It is worth being precise about that, because this directory sits next to a vCenter
# simulator and the pattern does not repeat here. The `dns` plugin targets an EXTERNAL
# authoritative DNS server in production, and what makes RFC 2136 the right transport is
# that it belongs to no vendor (ADR-0144 D2). Standing up a simulator would have proved
# the plugin talks to our own mock; standing up BIND proves it talks to the reference
# implementation of the protocol, which is the only version of that claim worth having.
#
# Built rather than pulled because the zone and the TSIG key are the harness's, and
# because there is no Docker Official Image for BIND to pull from the mirror this repo
# uses. The package is Alpine's `bind`, which is upstream ISC BIND 9 (MPL-2.0, a separate
# process we speak a protocol to — no linking, no copyleft reach into the control plane).
FROM public.ecr.aws/docker/library/alpine:3.24

# bind = named (the server). bind-tools brings dig/nsupdate, which is what makes this
# image able to FALSIFY the plugin: a proof that only ever queries through our own client
# cannot tell "the record was written" from "our reader agrees with our writer".
RUN apk add --no-cache bind bind-tools

# The zone lives on a writable volume-ish path because dynamic update means named
# rewrites it, plus a journal beside it. /var/bind is Alpine's own default directory.
RUN mkdir -p /var/bind /var/run/named && chown -R named:named /var/bind /var/run/named

COPY named.conf.tmpl /etc/bind/named.conf.tmpl
COPY zone.tmpl /etc/bind/zone.tmpl
COPY entrypoint.sh /usr/local/sbin/entrypoint.sh

EXPOSE 53/tcp 53/udp
ENTRYPOINT ["/usr/local/sbin/entrypoint.sh"]
