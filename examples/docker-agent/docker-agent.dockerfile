# v2's sandbox.image, as content. The template carries the platform floor
# and the docker-agent install; Docker publishes the agent through the
# template, so the workload ships it as-is and the kit's version: field
# speaks for the artifact.
FROM dhi.io/sbx-templates:docker-agent-docker
# v2's environment.variables.
ENV TERM=xterm-256color COLORTERM=truecolor LANG=en_US.UTF-8 \
    DOCKER_AGENT_AUTO_UPDATE=1 DOCKER_AGENT_NO_TOUR=1 \
    DOCKER_AGENT_HIDE_TELEMETRY_BANNER=1
USER agent
WORKDIR /home/agent/workspace
# v2's sandbox.entrypoint. --agent-picker needs the interactive terminal
# a TTY launch provides.
ENTRYPOINT ["docker-agent", "run", "--yolo", "--agent-picker"]
