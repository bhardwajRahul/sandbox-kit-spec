# Claude over ACP

This sandbox is a published *set*: one kit whose content was merged from
a shell base, Claude Code, and the Agent Client Protocol adapter. Run
`cat /usr/share/sandbox/kit/claude-acp-set/kit.yaml` to see the three, each pinned
by digest, and `ls /usr/share/sandbox/kit/` to see their own sources
staged beside each other.

Two ways to work here, and they are the same agent either way:

- Interactively, `claude` — the binary at `/usr/local/bin/claude`.
- Over ACP, `claude-agent-acp` on stdio, which an editor spawns and
  speaks the protocol to. It execs the same binary, so a session driven
  from an editor and one typed at the terminal share the version, the
  credential, and the session-state volumes.

The adapter opens no sockets of its own. Its egress and its Anthropic
credential are the ones Claude Code already has, which is why the merged
network policy names `api.anthropic.com` once rather than twice.
