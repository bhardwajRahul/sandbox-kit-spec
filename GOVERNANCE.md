# Governance

Docker maintains this specification. The people accountable for it are in
[MAINTAINERS](MAINTAINERS); GitHub routes review to them through
[CODEOWNERS](.github/CODEOWNERS).

## How decisions are made

Changes land by pull request with a maintainer's approval. Discussion
belongs in the issue or the pull request, so the reasoning stays next to
the change that carries it.

Anyone may propose a change. A proposal that alters the grammar, a
capability contract, or a conformance duty is judged on whether it can be
stated normatively and checked mechanically — see the rules below — not
on who proposed it.

## What a specification change must carry

The specification is not prose alone: in SPEC-v3 and the capability
pages, every normative statement is anchored and then accounted for — by
a check that judges it or by a recorded waiver saying why it cannot be
judged yet — and the suites fail when that accounting slips. A change to
SPEC-v3 or a capability page therefore arrives with:

- when the change touches the grammar — a descriptor field, a capability
  config — the JSON Schema and the Go types in `spec/` updated together,
  since `spec/schema_test.go` pins one against the other. A page that
  only changes a runtime duty needs neither;
- a `<!-- tck: <id> -->` anchor on every new
  **MUST**/**MUST NOT**/**SHOULD**/**SHOULD NOT**, the four keywords the
  guard reads, and either a check that judges it or a recorded waiver
  explaining what cannot be observed yet;
- for an **observable** runtime duty, a fixture and a fake-adapter
  mutation, so the suite proves the check fails when the behavior is
  absent. A duty the suite cannot observe records a waiver instead, and
  several do.

`docs/spec/conformance.md` is outside that accounting by design: it binds
adapters and the suite itself, which the harness enforces by
construction rather than by anchor.

A change that cannot be checked is not rejected for that reason alone,
but it must say so in its waiver rather than appear covered.

## Versioning

[RELEASES.md](RELEASES.md) describes the version axes and when each one
moves.

## Conduct

Participation is governed by the [Code of Conduct](.github/CODE_OF_CONDUCT.md).
Security reports follow [SECURITY.md](.github/SECURITY.md) rather than public
issues.
