# Releases

Several version axes move independently in this repository; confusing them
is the main hazard. This page says what each one means and what forces it
to change. Schema-3 frontend releases (`v3.*` tags) share the git tag
namespace with module tags but are not Go module versions.

## Version axes

| Axis | Where it lives | Moves when |
|---|---|---|
| Module tag | git tags `v0.X.Y` (until `v1.0.0`) | Any release of the Go packages (`spec`, `resolve`, `assemble`, `tck`) |
| Schema / frontend release | git tags `v3.X.Y` (+ Hub `docker/sandbox-kit:3.X.Y`) | A publish of the schema-3 BuildKit frontend and `kit-tck` binaries |
| Schema version | `spec.SchemaVersion`, the descriptor's `schemaVersion: "3"` | The descriptor grammar changes shape incompatibly |
| Capability version | the `@N` in `com.docker.sandbox/<name>@N` | That capability's config schema changes after it has shipped |
| Frontend floating tag | `docker/sandbox-kit:3` | Tracks the highest stable schema-3 frontend release |

A kit's own `version:` and its `provides` entries are a further axis, but
they belong to kit authors rather than to this repository;
[SPEC-v3 §5.2](docs/spec/SPEC-v3.md#52-versions) governs them.

`v3.*` git tags are **not** Go module majors. The module path is
`github.com/docker/sandbox-kit-spec` without a `/v3` suffix; module
consumers must use `v0.*` / `v1.*` tags (or a commit). A `v3.*` tag
publishes the frontend image and attaches `kit-tck` release assets — it
does not change how `go get` resolves this module.

## Module tags

Tag the module when the Go packages should be consumable at a new
version: `git tag -s v0.X.Y && git push origin v0.X.Y`. Tags are
annotated and signed. Until `v1.0.0` the minor position absorbs breaking
changes, as Go's own pre-1.0 convention allows. Do not retag the module
as `v3.*` without also changing the module path to `…/v3`.

## Schema version

`schemaVersion: "3"` names the generation of the descriptor grammar.
Decoding is strict ([SPEC-v3 §1.2](docs/spec/SPEC-v3.md#12-strict-decoding)),
in two passes with two mechanisms. The descriptor itself is YAML-decoded
with `KnownFields(true)` (`spec.Decode`), which rejects an unrecognized
top-level field. A capability's `config` survives that pass as a plain
map and is decoded per type later, with `DisallowUnknownFields`
(`spec.DecodeCapabilityConfig`), which rejects an unrecognized key in a
well-known type's config. Config for a type the reader does not know is
the exception at both steps — it stays an opaque map, which is what lets
third-party types travel.

For anything the grammar defines, then, the compatibility question is not
"would an old reader ignore this?" — it would not; it would refuse the
document.

What that buys is a grammar where a descriptor is either understood
completely or rejected loudly. What it costs is that **adding a field is
not free**: a descriptor using one cannot be read by a frontend or
runtime built before it. Adding a field to a capability's config moves
that capability's version, which is the fine-grained lever; moving
`schemaVersion` is for changes that lever cannot express — a top-level
field added, renamed, or removed, or a different meaning for one that
stays. An addition counts: strict decoding means an older reader refuses
a descriptor that uses it.

Moving it is expensive: the frontend image tag follows, every `#
syntax=docker/sandbox-kit:N` line in the wild points at the old one, and
`spec.SchemaVersion` gates decoding. Treat it as a new specification
document (`docs/spec/SPEC-v4.md`) rather than an edit to the current one.

## Capability versions

Each capability type addresses its own config schema by version, so
**the version moves when that config schema changes** — the rule
[SPEC-v3 §7](docs/spec/SPEC-v3.md#7-capabilities) states — and the old
version stays published: `network-policy@1` and `@2` both exist, and a
descriptor states one of them.

"Changes" is not only "gains a field an old runtime would reject on
decode". A field whose meaning, default, or permitted values change is
worse, because an old runtime accepts it and then enforces the wrong
policy — a silent misreading of a permission grant rather than a loud
failure. Both move the version.

The rule has one carve-out worth stating plainly, because it recurs in
review: a capability that has never appeared in a tagged release has no
runtime built against it, so its schema may still change in place. The
moment it ships, that freedom ends.

Adding a whole new capability type is additive and moves nothing.

## Frontend image

Pushing a `v3.X.Y` (or `v3.X.Y-rc.N`) tag runs the release workflow, which
publishes `docker/sandbox-kit:3.X.Y` and, when that tag is the highest
stable `v3.*.*` on the remote, also moves floating `docker/sandbox-kit:3`.
Main commits publish `docker/sandbox-kit:<short-sha>` only.

To publish by hand:

```sh
FRONTEND_PUSH_OK=1 task frontend:push FRONTEND_VERSION=3.0.0
# also move floating :3 (only when this is the intended tip):
FRONTEND_PUSH_OK=1 FRONTEND_PROMOTE_MAJOR=1 \
  task frontend:push FRONTEND_VERSION=3.0.0
```

`FRONTEND_PUSH_OK` is a precondition, not decoration: these tags are the
syntax references descriptors resolve, so publishing is never one
forgotten flag away. Kits name the floating major in their `# syntax=`
line, so `:3` must keep building every descriptor of that generation —
rebuild and promote it whenever the grammar gains something kits may
use, and never repoint it at a frontend that would reject an older v3
descriptor.

## Conformance suites and releases

`task tck:runtime ADAPTER=<path>` judges a runtime through its adapter,
and `task tck:kit REF=<ref>` judges a published artifact; `task test:tck`
runs the same runtime suite against the repository's fake adapter, which
is how the suite itself is kept honest. All are versioned with the module
rather than separately — a release of the Go packages is also the release of the
conformance suites, and a suite that gains a check can fail a runtime
that passed the previous tag. That is intended: the check reflects a duty
the specification already stated.
