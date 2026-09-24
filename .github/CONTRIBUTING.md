# Contributing

Thanks for your interest in the Docker Sandbox Kit Specification.

## Developer Certificate of Origin

Contributions are accepted under the [Developer Certificate of
Origin](https://developercertificate.org/). You certify that you wrote the
patch or have the right to contribute it, by signing off each commit:

```sh
git commit -s
```

which appends a `Signed-off-by` trailer matching your `user.name` and
`user.email`.

## Building and testing

The repository uses [Task](https://taskfile.dev). `task --list` shows
everything; the three you need for a change are:

```sh
task validate   # gofmt + go vet across the module
task lint       # golangci-lint + markdownlint, the same configs CI uses
task test       # all Go tests
```

CI runs these on every pull request, so a clean local run is the same
verdict rather than a different one.

To exercise a change to the BuildKit frontend, build it under a throwaway
tag and point an example at it — BuildKit caches frontend resolution per
reference, so rebuilding a reused tag like `:3` can keep dispatching the
stale binary:

```sh
task kit:dev KIT=hello
```

## Changing the grammar

The descriptor grammar has four representations that must move together,
and tests enforce it:

- `spec/` — the Go types, strict decoding, and validation. This is the
  source of truth; on any disagreement, the code wins.
- `schema/kit.schema.json` and `schema/capabilities/` — the JSON Schema
  used for editor validation. `spec/schema_test.go` pins its constants
  against the spec package so the two cannot drift silently.
- `docs/spec/SPEC-v3.md` and `docs/spec/capabilities/` — the normative
  specification. A capability page states the behavior a conforming
  runtime implements, so a behavior change belongs there too.
- `examples/` — every descriptor under `examples/` must decode and
  validate against the current grammar.

The agent skills under `skills/` teach the grammar too, and nothing tests
them, so a grammar change updates them by hand in the same pull request.

Bear in mind that decoding is strict: any unrecognized field is an error.
Renaming or removing a field therefore breaks kits already published under
the old spelling, which have to be rebuilt rather than degrading. Say so
explicitly in the pull request when a change has that shape.

## Pull requests

Use [Conventional Commits](https://www.conventionalcommits.org/) for commit
subjects (`fix(spec): …`, `feat(frontend): …`). Keep a change and its tests
in the same commit, and separate unrelated fixes into separate commits so
they can be reviewed and reverted independently.
