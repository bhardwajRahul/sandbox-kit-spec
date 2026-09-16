# GitHub CLI

`gh` is on PATH at `/usr/local/bin/gh`.

- Authentication is proxy-managed: `GH_TOKEN` is set to a sentinel and the
  real credential is injected on requests to github.com, api.github.com,
  and uploads.github.com. Never print or copy the token; it is not the
  real value anyway.
- Do not run `gh auth login` — authentication already works.
- Prefer `gh` over raw git for GitHub operations: `gh pr create`,
  `gh issue list`, `gh run watch`, `gh api` for anything without a
  dedicated subcommand.
