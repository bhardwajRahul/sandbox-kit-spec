# v2's sandbox.image, as content. The template carries the platform floor
# and a codex install; the build re-pins codex to the release this kit
# publishes, so the provide cannot claim a version the image does not ship.
# codex-code-mode-host rides along: codex spawns it as a sibling executable
# for Code Mode and fails that feature closed when it is missing.
FROM dhi.io/debian-base:trixie-dev AS build
ARG CODEX_VERSION
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates
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

FROM dhi.io/sbx-templates:codex-docker
COPY --from=build /out/usr/local/bin/ /usr/local/bin/
# v2's environment.variables, in the slot OCI already owns for static env.
ENV IS_SANDBOX=1 BROWSER=xdg-open CODEX_HOME=/home/agent/.codex GIT_TERMINAL_PROMPT=0
USER agent
WORKDIR /home/agent/workspace
# v2's sandbox.entrypoint.
ENTRYPOINT ["codex", "--dangerously-bypass-approvals-and-sandbox"]
