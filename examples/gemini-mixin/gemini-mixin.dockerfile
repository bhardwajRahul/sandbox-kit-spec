# syntax=docker/dockerfile:1
# gemini-cli is a Node app and the shell base ships no Node, so the
# overlay carries the node binary plus the globally installed package —
# the same shape as the claude-acp mixin. dhi.io/node:24-debian13-dev is
# the hardened Node image: debian13 is trixie, matching the glibc floor
# of the bases these examples compose onto, and the -dev variant is the
# one carrying npm and a shell. The MCP merge hook
# needs jq, which the shell base ships.
FROM dhi.io/node:24-debian13-dev AS build
ARG GEMINI_VERSION
RUN npm install -g --prefix /opt/gemini "@google/gemini-cli@${GEMINI_VERSION}"

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
COPY --from=build /opt/gemini /opt/gemini
# The launcher and v2's environment.variables, riding the overlay; the
# base's login shell sources profile.d.
COPY --chmod=755 <<'EOF' /usr/local/bin/gemini
#!/bin/sh
exec /usr/local/bin/node /opt/gemini/lib/node_modules/@google/gemini-cli/dist/index.js "$@"
EOF
COPY <<'EOF' /etc/profile.d/gemini-env.sh
export BROWSER=xdg-open
export DISPLAY=:0
export SANDBOX=docker
export GEMINI_SANDBOX=false
EOF
