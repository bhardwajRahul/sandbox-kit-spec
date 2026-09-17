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

# The marker records the violation, not the run: the host reads this
# entrypoint and launches the agent through it later, so a conforming
# runtime does execute it. What it must never be is the container's init,
# which would prepend it to whatever the host meant to run.
COPY --chmod=755 <<'EOF' /usr/local/bin/kit-tck-sbx-entrypoint
#!/bin/sh
[ "$$" -eq 1 ] && : > /var/tmp/sbx-entrypoint-ran
exec sleep infinity
EOF

ENV BASH_ENV=/etc/sbx-persistent.sh
USER sbxagent
WORKDIR /home/sbxagent/workspace
ENTRYPOINT ["/usr/local/bin/kit-tck-sbx-entrypoint"]
