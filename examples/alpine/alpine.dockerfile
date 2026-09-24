# syntax=docker/dockerfile:1
# Minimal Alpine workload on the DHI alpine-base (dev) variant. The base
# already runs as root and ships busybox sh, apk, and a CA bundle; it does
# not ship bash. sbx@1 requires /bin/bash for agent launch via BASH_ENV, and
# the platform floor also expects curl and git — install those, stay root,
# and do not create an agent user.
FROM dhi.io/alpine-base:3.24-dev

# The runtime's CA installer writes the egress proxy's CA to
# /usr/local/share/ca-certificates/proxy-ca.crt under `set -e` before merging
# it into the bundle; alpine-base ships no such directory, so without it the
# merge never happens and every TLS connection through the proxy (apk
# included) fails as untrusted.
RUN apk add --no-cache bash curl git \
 && mkdir -p /root/workspace /usr/local/share/ca-certificates \
 && touch /etc/sandbox-persistent.sh

ENV BASH_ENV=/etc/sandbox-persistent.sh
USER root
WORKDIR /root/workspace
ENTRYPOINT ["bash"]
CMD ["-l"]
