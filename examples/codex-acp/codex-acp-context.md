## Codex ACP adapter

`codex-acp` is installed at `/usr/local/bin/codex-acp`. It is an Agent
Client Protocol (ACP) server on stdio: a client spawns it and drives a
Codex session through the protocol. It is not an interactive command —
with no client on the other end of stdio it sits silent, so do not run it
from this shell to "check" it.

Host tools must attach it to a non-TTY stdin/stdout stream, for example
`sbx exec -i <sandbox> codex-acp`. In Zed, configure an agent server
whose command runs that.

The adapter drives the same `codex` binary this sandbox already has
(`CODEX_PATH=/usr/local/bin/codex`), so authentication is the surface
`codex` uses: the proxy-mediated OpenAI credential behind
`~/.codex/auth.json` and the `~/.codex/config.toml` this composition
seeds. If `codex` works here, the adapter works with no extra setup and
nothing separate to log in to. Sessions created through ACP and
interactive `codex` runs share the same `~/.codex` state, which this
composition keeps in the sandbox filesystem rather than on a volume.
