Codex CLI is installed at `/usr/local/bin/codex`. Run `codex` for an
interactive session or `codex exec "<prompt>"` for a one-shot run.

Authentication is proxy-mediated: `OPENAI_API_KEY` in this container is a
sentinel — the sandbox proxy substitutes the real key bound on the host
(`sbx secret set openai`) on requests to `api.openai.com`. Without a
bound key, `codex` will ask you to log in.
