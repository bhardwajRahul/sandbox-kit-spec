---
name: migrate-kit-to-v3
description: >-
  Migrates a Docker sandbox kit from the v2 `spec.yaml` grammar to the v3 kit
  descriptor, then builds it with docker buildx, runs it with sbx, and verifies
  it with the kit-tck conformance suite. Use when migrating or porting a kit to
  schemaVersion 3, when converting a v2 spec.yaml / sandbox.image / setup hooks
  / permissions block into v3 capabilities, when adding a `-mixin` variant of a
  workload kit, or when asked how to build, run, or conformance-check a v3 kit.
---

# Migrate a kit to v3

A v2 kit is one `spec.yaml` plus a `Dockerfile` that builds a named image. A v3
kit is one OCI image: the descriptor declares what the kit needs as typed
capabilities and rides in a manifest annotation, the image config carries the
runtime contract (entrypoint, env, user, workdir), and the layers carry the
content.

## Tooling

Three published tools, nothing repo-local:

- **`docker buildx`** builds kits. Nothing to install for the kit frontend —
  BuildKit pulls `docker/sandbox-kit:3` from the descriptor's `# syntax=` line.
- **`sbx`** runs them. Kit v3 is not in the stable line yet, so install a
  release candidate or nightly from
  [sbx-releases](https://github.com/docker/sbx-releases).
- **`kit-tck`** judges conformance:
  `go install github.com/docker/sandbox-kit-spec/v3/cmd/kit-tck@latest`, or take
  an archive from the
  [releases page](https://github.com/docker/sandbox-kit-spec/releases). (A
  `go install` build reports its version as `dev`; the release archives carry
  the real version string.)

  **This repository is private, so both routes need access to it.** The public
  module proxy cannot serve it — `proxy.golang.org` answers 404 — so `go
  install` resolves direct and needs a git credential plus
  `GOPRIVATE=github.com/docker/*`. In CI that means a token with read access,
  and a fork's token does not have one: a fork can still validate a descriptor
  by building it, since the frontend validates during the build, but it cannot
  run `kit-tck`. If the repository becomes public, that whole constraint
  disappears.

## Authorities

In precedence order:

1. [`spec/`](https://github.com/docker/sandbox-kit-spec/tree/main/spec) — the Go
   implementation. Where docs and code disagree, code wins.
2. [SPEC-v3.md](https://github.com/docker/sandbox-kit-spec/blob/main/docs/spec/SPEC-v3.md)
   — the grammar (§3 authoring forms, §4 top-level fields, §5
   provides/requires, §6 args, §7 capabilities, §8 launch modes).
3. [capability pages](https://github.com/docker/sandbox-kit-spec/tree/main/docs/spec/capabilities/com.docker.sandbox)
   — one per capability type, normative for its config schema and runtime
   behavior.
4. [examples](https://github.com/docker/sandbox-kit-spec/tree/main/examples) —
   worked kits. `claude` and `claude-mixin` are the migration of a real v2 kit
   into both shapes; read them first.

Working inside the spec repo, all four are also on disk at `spec/`,
`docs/spec/` and `examples/`.

## Workflow

Track progress with this checklist:

```text
- [ ] 1. Read the v2 kit end to end, including its README and testdata
- [ ] 2. Write the v3 descriptor, recipe, and context file
- [ ] 3. Add the -mixin variant (workload kits only)
- [ ] 4. Validate the descriptor (fast loop, seconds per run)
- [ ] 5. Build the kit
- [ ] 6. Run it with sbx and exercise the agent
- [ ] 7. Verify with kit-tck
```

### 1. Read the v2 kit first

Read `spec.yaml`, `Dockerfile`, `README.md` and `testdata/tck.yaml` before
writing anything. v2 kits carry their reasoning in comments, and that prose is
the most valuable thing to carry across. `testdata/tck.yaml` is the only record
of whether a working non-interactive invocation was ever established
(`promptArgs`), which decides whether the migrated kit declares
`agent-sessions@1`.

### 2. Write the v3 files

For a kit named `<kit>`, migrating in place:

| v2 | v3 |
|---|---|
| `<kit>/spec.yaml` | `<kit>/<kit>.yaml`, first line `# syntax=docker/sandbox-kit:3` |
| `<kit>/Dockerfile` | `<kit>/<kit>.dockerfile` — found by filename stem, so no `dockerfile:` field |
| `agentInstructions.content` | `<kit>/<kit>-context.md`, referenced as `contentFile: ./<kit>-context.md` |
| `<kit>/testdata/tck.yaml`, `<kit>/.dockerignore` | delete — v2-only harness and build wiring |
| `<kit>/<kit>_tck_test.go` | delete — it loads the v2 `spec.yaml` through `tck.NewSuiteFromDir(".")` and cannot compile once that file is gone |

The complete field-by-field mapping, the capability rules, and the gotcha list
are in [FIELD-MAPPING.md](FIELD-MAPPING.md). Read it before writing the
descriptor.

Migrate faithfully: preserve every declared host, credential, hook, volume,
port, env var and instruction, and keep base images verbatim. Where v3 cannot
express something, or where a v2 declaration turns out to be dead config, mark
it with an inline `# MIGRATION NOTE:` comment rather than dropping it silently
— the `claude` example shows the convention.

Faithful does not mean literal in one respect: a v2 install **hook** is often a
v2 limitation rather than a v3 requirement, because a v2 mixin had no way to
ship content. Decide per hook whether it becomes a layer — the rule, and the
five cases where a hook is still correct, are in
[FIELD-MAPPING.md](FIELD-MAPPING.md#lifecycle1).

### 3. Add the `-mixin` variant

Every workload kit gets a sibling `<kit>-mixin/` holding `<kit>-mixin.yaml`,
`<kit>-mixin.dockerfile` and `<kit>-mixin-context.md`. The mixin declares the
same credentials, network policy, volumes and hooks, minus what only the kit
that owns the environment can carry. See the
[mixin variants](FIELD-MAPPING.md#mixin-variants) section for the exact
subtractions and the overlay recipe patterns.

### 4. Validate the descriptor

The frontend decodes and validates the descriptor **before** it builds any
content, so a build that produces nothing is the fast validation loop:

```sh
cd <kit> && docker buildx build . -f <kit>.yaml --output type=cacheonly
```

A malformed descriptor fails in about a second, naming the offending field and
its line. Content only builds once the descriptor is valid, so iterate here
until it is clean — this is seconds per run where a full build is minutes.

Pass build-phase args by the **kit's** arg name, not the `buildArg` name the
recipe sees, and supply anything declared `required` or validation fails:

```sh
docker buildx build . -f <kit>.yaml --build-arg version=2.99.0 --output type=cacheonly
```

This judges the descriptor and the recipe. Nothing checks the recipe's `FROM`
and `COPY --from` correctness for you until the content actually builds in
step 5.

### 5. Build the kit

Drop `--output` for an ordinary tagged image:

```sh
docker buildx build . -f <kit>.yaml -t <kit>-kit:<tag>
```

To judge the artifact with `kit-tck` without a registry, export an OCI layout
directory instead:

```sh
docker buildx build . -f <kit>.yaml -t <kit>-kit:<tag> \
  --output type=oci,dest=/tmp/<kit>-layout,tar=false
```

A workload's recipe must build on a base carrying the platform floor — `bash`,
the `agent` user (uid 1000), `git`, a CA store — which the published
`docker/sandbox-templates:*` images carry. A bare distro base builds fine and
fails at agent launch.

### 6. Run it with sbx

Point `sbx` at the kit directory — the runtime builds source-form kits on
demand, keyed by source hash, so this needs no registry and no push:

```sh
sbx run ./<kit> .                            # a workload kit
sbx run ./<workload> --kit ./<kit>-mixin .   # a mixin, composed onto a workload
```

A mixin cannot run alone; compose it onto the migrated workload or onto a shell
workload. Pass kit args with `--kit-arg name=value` (or `--kit-arg
kit.name=value` to target one kit), and bind a credential the kit declares with
`sbx secret set -g <service>` before expecting authenticated calls to work.

Inside the sandbox, the kit is self-describing — use it to check that what you
declared is what arrived:

```sh
cat /usr/share/sandbox/kit/<kit>/kit.yaml        # the published descriptor
cat /usr/share/sandbox/kit/<kit>/kit.dockerfile  # the recipe that built it
cat /var/log/sbx-kit-startup.log                 # startup hook output
```

Then exercise the kit for real: run the agent's own version command, confirm
install hooks left what they should, confirm a declared volume is writable by
`agent`, and confirm an undeclared host is refused while a declared one is not.

`sbx kit inspect ./<kit>` builds the source kit and prints its resolved
declarations (kind, network counts, credentials, args) without starting a
sandbox. Add `--kit-arg` to preview how args resolve. The first source build
creates the shared builder sandbox and is slow; `sbx kit builder status` shows
it and `sbx kit builder rm` reclaims the cache.

For the published path instead of the local loop, push the kit as an ordinary
image and run it by reference — a kit image that only exists in the local
Docker store cannot run, because the runtime resolves kit images from
registries:

```sh
docker buildx build . -f <kit>.yaml --push -t docker.io/<you>/sbx-kit-<kit>:<tag> \
  --platform linux/amd64,linux/arm64 --provenance=true
sbx run docker.io/<you>/sbx-kit-<kit>:<tag> .
```

### 7. Verify with kit-tck

`kit-tck` judges an artifact's annotations, layers, staged sources and image
config, with every check linked to the clause it enforces. The same checks run
inside the frontend during a build, so running them here is how an artifact
changed by an exporter or a registry on its way out gets judged — and how a kit
this frontend did not build gets judged at all.

```sh
kit-tck kit --layout /tmp/<kit>-layout <tag>          # the OCI layout from step 5
kit-tck kit docker.io/<you>/sbx-kit-<kit>:<tag>       # a published kit
```

For the layout form, `<tag>` is the tag **alone** as the layout records it
(`1.0.0`), not the full `<kit>-kit:1.0.0` reference the build was tagged with.

Add `--verbose` to list the checks that passed, `--format json` for every check
with its spec link, and `--plain-http` for a registry served over HTTP.

A published kit built on a **multi-node** builder warns that the index carries
no kit annotations. That is expected, not a defect: a multi-node build merges
per-node results into a fresh index client-side, which dissolves them, and
§9.3 requires consumers to fall back to the platform manifest, which does carry
them. The verdict is still `✓ conforms`. A single-node build shows no warning.

Runtime conformance is a separate suite, for people implementing a runtime
rather than authoring a kit: `kit-tck runtime --adapter <path>` drives hundreds
of sandbox lifecycles against an adapter implementing
[conformance.md](https://github.com/docker/sandbox-kit-spec/blob/main/docs/spec/conformance.md).
It takes hours, and migrating a kit does not need it.

## What actually catches bugs

Each check below caught real defects in a migration of 87 kits that the
cheaper checks above it did not. They are ordered by what they cost.

1. **Validation** catches malformed descriptors and nothing else. It cannot see
   a `contentFile:` pointing at a missing file, a `*-context.md` no descriptor
   references, a v2 instruction body that was dropped, or an authored `deb/`
   provide — that last one is refused by the **build**, not by validation.
   Script the file-level audits; they are seconds and they found a kit whose
   instructions would silently never have reached the agent.
2. **Building** catches recipes. It does not prove the content works.
3. **Reading the exported layer** catches ownership. Export with
   `--output type=oci,dest=<dir>,tar=false` and count owners:
   `for b in <dir>/blobs/sha256/*; do tar tvf "$b"; done | awk '{print $3":"$4}' | sort | uniq -c`.
   Everything should be `0:0` or `1000:1000`; `home/` must be `0 0` and
   `home/agent/` `1000 1000`. This found six overlays that gave `/home` away or
   took `$HOME` from the agent, and four shipping files owned by package
   publishers' uids.
4. **Composing the overlay onto a bare base and running the tool** catches the
   rest, and nothing else does. Three mixins shipped dangling symlinks whose
   build-time `test -x` passed because the real tree was still present *in the
   build stage*: the installer had relocated a launcher but not its payload.
   Build with `--load`, then a throwaway `FROM ubuntu:24.04` plus
   `COPY --from=<overlay> / /`, and run the tool as uid 1000.
5. **`kit-tck`** judges the published artifact — see step 7.

When you change ownership, re-run step 4, not just step 3. A `chown` that fixes
the numbers can still break the tool.

## Known gaps

- `sbx kit validate` does not accept a v3 **source** kit: its load path has no
  kit builder configured. Validate with the build in step 4, and inspect the
  built artifact with `sbx kit inspect`.
- A migration strands every script, workflow and test that globs the old
  layout. Audit them as part of the work: discovery globbing `*/spec.yaml`
  returns nothing, so CI passes by building nothing at all, which is the worst
  failure mode available.

## Migration conventions

These are the conventions the `sbx-kits-contrib` v3 migration followed. Keep
them unless the task says otherwise:

- Migrate in place and delete the v2 `spec.yaml` and `Dockerfile` once the v3
  pair exists.
- Keep each kit's base images verbatim; a grammar migration is not the moment
  to re-point a base.
- Keep `README.md`, updating the filenames and any v2 grammar it quotes; keep
  `README.image.md`; delete `testdata/tck.yaml` and `.dockerignore`.
- Heavily commented YAML is the house style. Carry the v2 comments across —
  they are the reasoning behind the declarations.
