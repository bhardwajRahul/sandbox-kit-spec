# syntax=docker/dockerfile:1
# The agent is a Node app and the shell base ships no Node, so the
# overlay carries the node binary plus the globally installed package.
# dhi.io/node:24-debian13-dev is the hardened Node image: debian13 is
# trixie, matching the glibc floor of the bases these examples compose
# onto, and the -dev variant is the one carrying npm and a shell.
#
# --omit=optional drops the SDK's vendored native claude binary (shipped
# as platform-specific optional deps of @anthropic-ai/claude-agent-sdk).
# It is dead weight here — CLAUDE_CODE_EXECUTABLE below points the
# adapter at the claude the claude-mixin kit installs — and omitting it
# keeps one claude in the sandbox: without the binary present the
# adapter cannot silently fall back to a second, SDK-pinned version.
FROM dhi.io/node:24-debian13-dev AS build
ARG ACP_VERSION
RUN npm install -g --omit=optional --prefix /opt/claude-agent-acp \
      "@agentclientprotocol/claude-agent-acp@${ACP_VERSION}"

FROM dhi.io/debian-base:trixie-dev
COPY --from=build /usr/bin/node /usr/local/bin/node
COPY --from=build /opt/claude-agent-acp /opt/claude-agent-acp
RUN ln -s /opt/claude-agent-acp/bin/claude-agent-acp /usr/local/bin/claude-agent-acp

# The adapter resolves the CLI it drives from CLAUDE_CODE_EXECUTABLE
# first, and only then from the SDK's vendored native binary. Pinning it
# to the composed claude is what makes the ACP path and an interactive
# `claude` the same agent, at the version this composition states. Image
# config rather than a profile.d drop on purpose: an ACP client spawns
# the adapter over non-TTY stdio (`sbx exec -i`), which never sources a
# login profile but does inherit the container environment.
ENV CLAUDE_CODE_EXECUTABLE=/usr/local/bin/claude
