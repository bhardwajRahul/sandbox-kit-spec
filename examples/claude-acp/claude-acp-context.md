## Claude Code ACP adapter

`claude-agent-acp` is installed at `/usr/local/bin/claude-agent-acp`. It
is an Agent Client Protocol (ACP) server on stdio: a client spawns it and
drives a Claude Code session through the protocol. It is not an
interactive command — with no client on the other end of stdio it sits
silent, so do not run it from this shell to "check" it.

Host tools must attach it to a non-TTY stdin/stdout stream, for example
`sbx exec -i <sandbox> claude-agent-acp`. In Zed, configure an agent
server whose command runs that.

The adapter drives the same `claude` binary this sandbox already has
(`CLAUDE_CODE_EXECUTABLE=/usr/local/bin/claude`), so authentication is
the surface `claude` uses: the proxy-mediated Anthropic credential in
`~/.claude/.credentials.json` and `~/.claude/settings.json`. If `claude`
works here, the adapter works with no extra setup and nothing separate to
log in to. Sessions created through ACP share the session-state volumes
with interactive `claude` runs.
