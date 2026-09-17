# The identity is uid 1234 named sbxagent, not the conventional uid 1000
# named agent: a runtime that hard-codes the convention passes against a
# conventional image no matter what it reads, so the fixture makes the
# two answers differ.
FROM docker/sandbox-templates:claude-code-docker

USER root
RUN groupadd --gid 1234 sbxagent \
 && useradd --create-home --uid 1234 --gid 1234 --shell /bin/bash sbxagent \
 && touch /etc/sbx-persistent.sh \
 && chown sbxagent:sbxagent /etc/sbx-persistent.sh

# The entrypoint records that it ran. The host owns PID 1 and launches the
# agent itself, so this marker must never appear; if it does, the image's
# entrypoint became PID 1 and the launch command would have prepended
# itself to whatever the host meant to run.
COPY --chmod=755 <<'EOF' /usr/local/bin/kit-tck-sbx-entrypoint
#!/bin/sh
: > /var/tmp/sbx-entrypoint-ran
exec sleep infinity
EOF

ENV BASH_ENV=/etc/sbx-persistent.sh
USER sbxagent
WORKDIR /home/sbxagent/workspace
ENTRYPOINT ["/usr/local/bin/kit-tck-sbx-entrypoint"]
