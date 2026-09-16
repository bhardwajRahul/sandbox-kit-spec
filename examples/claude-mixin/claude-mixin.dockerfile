# syntax=docker/dockerfile:1
FROM dhi.io/debian-base:trixie-dev AS build
ARG CLAUDE_VERSION
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates bash
# Anthropic's installer resolves the platform and fetches the standalone
# native binary (no Node runtime needed). It installs under $HOME, so a
# throwaway HOME keeps the overlay to exactly one file: the versioned
# binary the ~/.local/bin/claude symlink resolves to.
RUN HOME=/build bash -c 'curl -fsSL https://claude.ai/install.sh | bash -s -- "$CLAUDE_VERSION"' \
 && mkdir -p /out/usr/local/bin \
 && install -m 0755 "$(readlink -f /build/.local/bin/claude)" /out/usr/local/bin/claude \
 && /out/usr/local/bin/claude --version

# The overlay: one binary, landing on any base.
FROM scratch
COPY --from=build /out /
