# Team environment

This sandbox is a published *set*: one kit whose content was merged from
the kits listed in its descriptor's `kits:`. Run
`cat /usr/share/sandbox/kit/team/kit.yaml` to see that list, each entry
pinned by digest, and `ls /usr/share/sandbox/kit/` to see each of those
kits' own sources staged beside it.

Conventions for work in this sandbox:

- Open a pull request rather than pushing to `main`; `gh pr create` is
  available and authenticates through the host's GitHub credential.
- The sandbox's egress is the union of what those kits asked for, and
  their denials union the same way and win: a host is reachable when
  one of them allowed it and none of them denied it. A host this
  environment cannot reach was either declared by nobody, or denied by
  one of the kits that make it up.
