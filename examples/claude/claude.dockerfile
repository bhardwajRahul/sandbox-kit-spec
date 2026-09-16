# v2's sandbox.image, as content. The template carries the platform floor
# (bash, agent user, git, CA store) and a claude install; the build
# re-pins claude to the release this kit publishes, so the provide cannot
# claim a version the image does not ship.
FROM dhi.io/sbx-templates:claude-code-docker
ARG CLAUDE_VERSION
USER agent
RUN curl -fsSL https://claude.ai/install.sh | bash -s -- ${CLAUDE_VERSION}
# v2's environment.variables, in the slot OCI already owns for static env.
ENV IS_SANDBOX=1
WORKDIR /home/agent/workspace
# v2's sandbox.entrypoint.
ENTRYPOINT ["claude", "--dangerously-skip-permissions"]
