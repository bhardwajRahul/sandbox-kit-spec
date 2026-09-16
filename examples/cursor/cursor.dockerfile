# v2's sandbox.image, as content. The template carries the platform floor
# and the cursor-agent install; the workload publishes it as-is — Cursor's
# installer has no version pin, so the kit's own version: field speaks for
# the artifact rather than claiming a binary version.
FROM dhi.io/sbx-templates:cursor-agent-docker
# v2's environment.variables. AGENT_CLI_CREDENTIAL_STORE=memory keeps
# cursor from validating a file-backed sentinel and re-prompting login;
# auth bootstraps through the environment when host OAuth creds exist.
ENV IS_SANDBOX=1 AGENT_CLI_CREDENTIAL_STORE=memory
USER agent
WORKDIR /home/agent/workspace
# v2's sandbox.entrypoint.
ENTRYPOINT ["cursor-agent", "--yolo"]
