# syntax=docker/dockerfile:1
FROM dhi.io/debian-base:trixie-dev AS build
ARG CODEX_VERSION
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates
# Codex ships static musl binaries per architecture; each tarball contains
# one file named after the target triple. codex-code-mode-host rides along:
# codex spawns it as a sibling executable for Code Mode and fails that
# feature closed when it is missing.
RUN case "$TARGETARCH" in \
      amd64) target=x86_64-unknown-linux-musl ;; \
      arm64) target=aarch64-unknown-linux-musl ;; \
      *) echo "unsupported TARGETARCH: $TARGETARCH" >&2; exit 1 ;; \
    esac \
 && mkdir -p /out/usr/local/bin \
 && for bin in codex codex-code-mode-host; do \
      curl -fsSLO "https://github.com/openai/codex/releases/download/rust-v${CODEX_VERSION}/${bin}-${target}.tar.gz" \
      && tar -xzf "${bin}-${target}.tar.gz" \
      && install -m 0755 "${bin}-${target}" "/out/usr/local/bin/${bin}"; \
    done \
 && /out/usr/local/bin/codex --version

# The v2 kit's environment.variables block has no v3 field (v3 carries no
# static env grammar; the image config owns runtime env, and a mixin's
# config does not merge). The exports ride the overlay instead, sourced by
# the base workload's login shell. GIT_TERMINAL_PROMPT=0 keeps git from
# hanging on an interactive credential prompt in a headless sandbox.
RUN mkdir -p /out/etc/profile.d && cat > /out/etc/profile.d/codex-env.sh <<'EOF'
export BROWSER=xdg-open
export CODEX_HOME=/home/agent/.codex
export IS_SANDBOX=1
export GIT_TERMINAL_PROMPT=0
EOF

# The overlay: the binary and its profile.d exports, landing on any base.
FROM scratch
COPY --from=build /out /
