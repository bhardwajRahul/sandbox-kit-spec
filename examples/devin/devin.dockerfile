# v2's sandbox.image, as content. The template carries the platform floor
# and the devin install; Cognition publishes no pinnable install channel,
# so the workload ships the template's devin as-is and the kit's version:
# field speaks for the artifact.
# The hardened dhi.io/sbx-templates mirror carries every other
# agent template but not devin, so this one stays on the public
# tag until it is mirrored.
FROM docker/sandbox-templates:devin-docker
USER agent
WORKDIR /home/agent/workspace
# v2's sandbox.entrypoint.
ENTRYPOINT ["devin", "--permission-mode", "dangerous", "--respect-workspace-trust=false"]
