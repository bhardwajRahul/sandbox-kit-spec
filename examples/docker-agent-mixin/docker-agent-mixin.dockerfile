# syntax=docker/dockerfile:1
# Docker publishes the agent through the template image, so the template
# is the distribution: the overlay copies the binary it ships, at the
# /opt home the self-update flow expects, with the bin symlink beside it.
FROM dhi.io/sbx-templates:docker-agent-docker AS src

FROM dhi.io/debian-base:trixie-dev AS build
COPY --from=src /usr/local/bin/docker-agent /tmp/docker-agent
# The /opt tree is agent-owned (uid/gid 1000) because AUTO_UPDATE means
# the agent user replaces the binary in place; a root-owned home would
# fail every self-update in this shape.
RUN mkdir -p /out/opt/docker-agent/bin /out/usr/local/bin /out/etc/profile.d \
 && install -m 0755 /tmp/docker-agent /out/opt/docker-agent/bin/docker-agent \
 && chown -R 1000:1000 /out/opt/docker-agent \
 && ln -s /opt/docker-agent/bin/docker-agent /out/usr/local/bin/docker-agent \
 && printf 'export TERM=xterm-256color\nexport COLORTERM=truecolor\nexport LANG=en_US.UTF-8\nexport DOCKER_AGENT_AUTO_UPDATE=1\nexport DOCKER_AGENT_NO_TOUR=1\nexport DOCKER_AGENT_HIDE_TELEMETRY_BANNER=1\n' \
      > /out/etc/profile.d/docker-agent-env.sh

FROM scratch
COPY --from=build /out /
