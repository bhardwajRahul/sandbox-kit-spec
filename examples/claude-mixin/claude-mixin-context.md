Claude Code is installed at `/usr/local/bin/claude`. Run `claude` for an
interactive session or `claude -p "<prompt>"` for a one-shot answer.

Authentication is proxy-mediated: `ANTHROPIC_API_KEY` in this container is
a sentinel — the sandbox proxy substitutes the real key bound on the host
(`sbx secret set -g anthropic`) on requests to Anthropic's APIs. Without a
bound key, `claude` will ask you to log in.
