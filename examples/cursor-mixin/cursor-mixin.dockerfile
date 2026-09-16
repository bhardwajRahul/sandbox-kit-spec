# syntax=docker/dockerfile:1
# Cursor's installer resolves the platform and lays the agent under
# $HOME/.local — a versions tree plus a bin symlink. The overlay carries
# that tree at a stable /opt path with a bin shim, landing on any base;
# the installer has no version pin, so the kit's version: field speaks
# for the artifact.
FROM dhi.io/debian-base:trixie-dev AS build
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates bash
RUN HOME=/build bash -c 'curl -fsS https://cursor.com/install | bash' \
 && mkdir -p /out/opt /out/usr/local/bin \
 && cp -a /build/.local/share/cursor-agent /out/opt/cursor-agent \
 && target=$(readlink /build/.local/bin/cursor-agent | sed 's#^.*/.local/share/cursor-agent#/opt/cursor-agent#') \
 && ln -s "$target" /out/usr/local/bin/cursor-agent

# v2's environment.variables ride the overlay, sourced by the base's
# login shell.
RUN mkdir -p /out/etc/profile.d && printf 'export IS_SANDBOX=1
export AGENT_CLI_CREDENTIAL_STORE=memory
' > /out/etc/profile.d/cursor-env.sh

# The overlay: the agent tree and its bin shim, landing on any base.
FROM scratch
COPY --from=build /out /
