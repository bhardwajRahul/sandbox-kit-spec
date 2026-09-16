# v2's sandbox.image, as content. The template carries the platform floor
# and the opencode install; the build re-pins opencode to the release this
# kit publishes, so the provide cannot claim a version the image does not
# ship.
FROM dhi.io/sbx-templates:opencode-docker
ARG OPENCODE_VERSION
# Root, and fatal: a failed re-pin must fail the build rather than let
# the kit publish a version the template's older binary does not ship.
USER root
RUN npm install -g "opencode-ai@${OPENCODE_VERSION}" \
 && opencode --version
USER agent
WORKDIR /home/agent/workspace
# v2's sandbox.entrypoint.
ENTRYPOINT ["opencode"]
