# syntax=docker/dockerfile:1
# opencode is a Node app and the shell base ships no Node, so the overlay
# carries the node binary plus the globally installed package — the same
# shape as the claude-acp and gemini mixins.
# dhi.io/node:24-debian13-dev is the hardened Node image: debian13 is
# trixie, matching the glibc floor of the bases these examples compose
# onto, and the -dev variant is the one carrying npm and a shell.
FROM dhi.io/node:24-debian13-dev AS build
ARG OPENCODE_VERSION
RUN npm install -g --prefix /opt/opencode "opencode-ai@${OPENCODE_VERSION}"

# The final stage is the diff base, never exported content: the overlay
# is exactly the delta over dhi.io/debian-base:trixie-dev (node, the package, the
# launcher, profile.d). The official node binary is dynamically linked
# and the overlay deliberately carries no libc — like the claude-acp
# mixin, this composes onto bases whose glibc is at least trixie's
# (the hardened Node image's build target), which every base workload in
# these examples satisfies. There is no grammar to enforce a libc floor:
# requires/provides name kit capabilities, and base workloads provide
# none for their C library.
FROM dhi.io/debian-base:trixie-dev
COPY --from=build /usr/bin/node /usr/local/bin/node
COPY --from=build /opt/opencode /opt/opencode
COPY --chmod=755 <<'EOF' /usr/local/bin/opencode
#!/bin/sh
exec /usr/local/bin/node /opt/opencode/lib/node_modules/opencode-ai/bin/opencode "$@"
EOF
