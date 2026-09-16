# A sandbox kit's rootfs must provide the runtime's platform floor: bash
# (the agent start script and persistent-env mechanism are bash), the
# `agent` user (uid 1000), git for the workspace hooks, and a CA store for
# the proxy. The published sandbox template images carry all of it; a bare
# distro or busybox image does not, and the sandbox fails at agent launch
# with a missing /bin/bash.
FROM dhi.io/sbx-templates:claude-code-docker
ARG GREETING=world
# The template's default user is the unprivileged `agent`; root-owned paths
# need an explicit escalation, and the runtime user is restored after.
USER root
RUN echo "hello ${GREETING}" > /etc/motd
USER agent
ENTRYPOINT ["bash"]
