# syntax=docker/dockerfile:1
# The agent is a Node app and the shell base ships no Node, so the
# overlay carries the node binary plus the globally installed package.
# dhi.io/node:24-debian13-dev is the hardened Node image: debian13 is
# trixie, matching the glibc floor of the bases these examples compose
# onto, and the -dev variant is the one carrying npm and a shell.
FROM dhi.io/node:24-debian13-dev AS build
ARG ACP_VERSION
RUN npm install -g --prefix /opt/codex-acp \
      "@agentclientprotocol/codex-acp@${ACP_VERSION}"

# The adapter bundles codex as a regular dependency whose per-platform
# binaries arrive as aliased optional deps, which --omit=optional does
# NOT skip (unlike the claude adapter's vendored binary); they are
# deleted here instead. CODEX_PATH below points the adapter at the codex
# the codex-mixin kit installs, so the bundled one is dead weight — and
# with no binary present the adapter cannot silently fall back to a
# second, dependency-pinned version. The guard fails the build if a
# codex binary survives, so the property is enforced, not asserted.
RUN find /opt/codex-acp -type d -path "*/@openai/codex-*/vendor" -prune -exec rm -rf {} + \
 && ! find /opt/codex-acp -type f -name codex | grep -q .

FROM dhi.io/debian-base:trixie-dev
COPY --from=build /usr/bin/node /usr/local/bin/node
COPY --from=build /opt/codex-acp /opt/codex-acp
RUN ln -s /opt/codex-acp/bin/codex-acp /usr/local/bin/codex-acp

# The adapter resolves the CLI it drives from CODEX_PATH first, and only
# then from its bundled @openai/codex dependency. Pinning it to the
# composed codex is what makes the ACP path and an interactive `codex`
# the same agent, at the version this composition states. Image config
# rather than a profile.d drop on purpose: an ACP client spawns the
# adapter over non-TTY stdio (`sbx exec -i`), which never sources a login
# profile but does inherit the container environment.
ENV CODEX_PATH=/usr/local/bin/codex
