# GitHub over SSH

git can fetch from and push to GitHub over SSH (`git@github.com:owner/repo`)
with the user's key, through the agent at `$SSH_AUTH_SOCK`. Commits and
tags you make are signed through the same agent; Git may select a
different key from the one GitHub accepts for authentication.

- The agent logs in to github.com as `git` and makes git signatures. It
  refuses every other server, account, and kind of signature, so `ssh` to
  another host fails by design: do not work around it.
- GitHub's host keys are in `~/.ssh/known_hosts`. If ssh reports a changed
  host key, stop and tell the user; never remove the entry or disable
  host key checking.
- The key belongs to the user. Push only what the user asked for, and say
  which commits you made.
