# Git commit signing

Commits and tags you make are signed with the user's SSH key, through the
agent at `$SSH_AUTH_SOCK`. You do not need to pass `-S`.

- The agent signs git signatures only. It will not log in to servers or
  sign anything else, so do not try to use it for `ssh` or `ssh-keygen -Y
  sign` with another namespace.
- If `ssh-add -L` reports no agent or no identities, signing is not
  available: ask the user to reconnect the agent before committing.
- Signing a commit does not mean the user reviewed it. Say which commits
  you made.
