# Codex over ACP

This sandbox is a published *set*: one kit whose content was merged from
a shell base, Codex CLI, and the Agent Client Protocol adapter. Run
`cat /usr/share/sandbox/kit/codex-acp-set/kit.yaml` to see the three, each pinned
by digest, and `ls /usr/share/sandbox/kit/` to see their own sources
staged beside each other.

Two ways to work here, and they are the same agent either way:

- Interactively, `codex` — the binary at `/usr/local/bin/codex`.
- Over ACP, `codex-acp` on stdio, which an editor spawns and speaks the
  protocol to. It execs the same binary, so editor-driven and terminal
  sessions share the version, credential, configuration, and state.

The adapter opens no sockets of its own. Its egress and OpenAI credential
are the ones Codex already has, so the merged policy does not need a
second copy of either declaration.
