---
name: create-kit-v3
description: >-
  Authors a new Docker sandbox kit against the v3 descriptor — choosing workload
  or mixin, writing the descriptor and its content recipe, declaring the
  capabilities it needs, pinning the tool version, and verifying the result with
  docker buildx, sbx and kit-tck. Use when creating a new kit from scratch,
  adding a `-mixin` variant, packaging a CLI or agent as a sandbox kit, or
  asking what a kit descriptor should contain.
---

# Create a v3 kit

A kit is one OCI image. Its layers are the content and its manifest annotation
carries the **descriptor**: what the kit offers, what it needs from the host as
typed capability requests, and what it needs from other kits. An engine that
does not read the annotation runs it as an ordinary image.

To migrate an existing v2 `spec.yaml` kit rather than write a new one, use the
`migrate-kit-to-v3` skill instead; its `FIELD-MAPPING.md` is also the best
field-by-field reference if you are unsure what a given declaration means.

## Decide the kind first

Everything else follows from this.

| | `kind: workload` | `kind: mixin` |
|---|---|---|
| Layers are | a root filesystem | an overlay landing on a workload |
| Per composition | exactly one | zero or more |
| Owns | entrypoint, env, user, workdir, the agent-context `filename` | env, labels, ports and volumes, which merge |
| Must have content | yes | no — may be declaration-only |

Write a **workload** when you own the environment the agent runs in. Write a
**mixin** when you add a tool, a credential or a policy to somebody else's.
Most new kits should be mixins, and a tool worth shipping as a workload is
usually worth shipping as both — that is what the `claude`/`claude-mixin` pair
in the examples is.

A mixin cannot set `ENTRYPOINT`, `CMD`, `USER` or `WORKDIR` and have it take
effect: the workload anchors the composition and owns those contract fields.
The **additive** fields do merge — env, labels, ports and volumes — so a
mixin's `ENV` reaches the composed image, and `PATH` is appended rather than
replaced.

Prefer `ENV` for static environment. It is what the agent process sees, and
`sbx@1` launches the agent under `bash` via `BASH_ENV` precisely because
profile and rc files do not run for it — so an `/etc/profile.d/<kit>-env.sh`
drop reaches a terminal the user opens and misses the agent itself. Reach for
profile.d only when a value is likely to collide, because two mixins setting
one variable to different values is a hard composition failure, or when the
value genuinely only makes sense in an interactive shell.

## Layout

The default authoring form is a companion pair, found by filename stem:

```text
<kit>/
  <kit>.yaml           # the descriptor; first line `# syntax=docker/sandbox-kit:3`
  <kit>.dockerfile     # the content recipe (omit for a declaration-only mixin)
  <kit>-context.md     # agent-context body, referenced as contentFile:
  README.md
```

No `dockerfile:` field is needed — the stem convention finds it. Three other
forms exist (an inline `build:` block, a `# kit:` comment descriptor inside a
Dockerfile, and a `kind: set` list of other kits); see SPEC-v3 §3.

## Descriptor skeleton

```yaml
# syntax=docker/sandbox-kit:3
schemaVersion: "3"
kind: mixin
displayName: GitHub CLI
description: gh, installed from the official release tarball
sourceUrl: https://github.com/cli/cli
licenses: [MIT]

# One value drives the install, the provide, the published version and the tag.
version: "${{ kit.args.version }}"

args:
  version:
    default: "2.98.0"
    pattern: '^[0-9]+\.[0-9]+\.[0-9]+$'
    description: GitHub CLI release to install
    buildArg: GH_VERSION

provides: ["gh@${{ kit.args.version }}"]
# No requires: this overlay ships a release tarball and needs nothing from the
# workload. Add entries only for what the composed runtime must already have —
# see below, and note the comment under `requires` about refusing to compose.

capabilities:
  - type: com.docker.sandbox/network-policy@1
    config:
      runtime:
        allow: [github.com, api.github.com]

  - type: com.docker.sandbox/agent-context@1
    config:
      contentFile: ./gh-context.md
```

Every key is `lowerCamelCase` with acronyms title-cased (`sourceUrl`,
`apiKey`). Decoding is **strict**: an unrecognized key anywhere is an error,
which is deliberate — a misspelled key silently ignored would be a policy
silently absent.

## Capabilities

A capability is a typed request the host answers. `optional: true` means the
kit degrades without it; the default is required, which fails resolution
closed. Read the page for each type you emit — each is normative for its config
and for what a runtime must do. The ones you will reach for, and the rule most
often got wrong:

| Type | Use it for | Easy to get wrong |
|---|---|---|
| `network-policy@1` | egress by host | It is **phase-scoped**: an absent phase grants nothing. Hosts your install hooks reach go in `install`, hosts the running agent (or a startup hook) reaches go in `runtime`, hosts both reach go in both. |
| `network-policy@2` | egress bounded by HTTP method and path | Same phases, plus entries that grant only matching requests. Pick `@2` when a host should carry one API and not the rest; stay on `@1` when host alone is the grant. **Exclusive with `@1`** — declaring both is an error, and a bounded allow entry must name its hosts literally. `gh` and `hello` use `@2`. |
| `credential@1` | one service's auth | Entries are **required by default** — add `optional: true` unless the kit genuinely cannot run unauthenticated. Every `inject[].domain` must appear in the same phase's allow list, matched **exactly**: a `*.example.com` wildcard does not satisfy `api.example.com`. |
| `lifecycle@1` | install/startup hooks, staged files | Hook environments are **deny-by-default**. Declare every variable in `env:`, including ones only a child process reads — `curl`, `pip` and `npm` need `HTTP_PROXY`/`HTTPS_PROXY`, and `docker` needs `DOCKER_HOST`. |
| `volume@1` | persistent paths | Always set `size`. An unsized kit volume is formatted at 512 MiB, which is a cache or a package store running out of room mid-run rather than anything visible at create. |
| `agent-context@1` | instructions the agent reads | `filename:` is for the kit that owns the environment — a workload or a set, never a mixin. Use `contentFile:` for a static body, but inline `content:` when the body interpolates an arg — a staged body is never arg-expanded. |
| `sbx@1` | "launch this as an agent" | Workload-only, config-less — and enforced: a mixin declaring it fails validation. |
| `agent-skills@1` | the host's shared skills store | Only where the agent really reads skills from that path, and never where the kit ships content there — the mount would hide it. |
| `port@1`, `resources@1`, `privileged@1` | inbound ports, limits, elevation | Do not declare on speculation; `privileged@1` is the largest widening available. |

## Versions, provides and requires

**Pin the tool, and say so once.** Declare a build-phase `version` arg, wire it
through to the installer, and reference it from both `provides` and the
top-level `version:` — publishing expands all of it, so one value drives the
install, the matchable capability, `org.opencontainers.image.version` and the
published tag.

**A pinned provide is a claim about content, so make the build enforce it.**
Add a step that re-reads the installed version and fails on mismatch. Pinning
the provide without pinning the install is worse than floating: it asserts a
version the content may not have.

If you leave a provide unversioned, know what it resolves to: an explicit
`@version` wins, else a version-shaped consumption reference, else the
descriptor's `version:` — and `:latest` is not version-shaped. So an
unversioned provide under `version: "1.0.0"` publishes `<tool>@1.0.0`, the
kit's release number wearing the tool's name, and a lower-bound constraint will
not match it.

**`requires` is a closed-set check**: a name nothing in the composition
provides makes your kit refuse to compose *anywhere*, which is worse than
saying nothing. It constrains the composed **runtime** set, not your builder —
a mixin that only touches apt inside a build stage needs no `deb/apt`, and
adding one there rejects every Alpine or distroless workload that could
otherwise have run the shipped binary perfectly well.

Two things follow, and both cut against the instinct to list whatever a
lifecycle hook shells out to:

**Never require the platform floor.** §12 lets kit content assume `bash` and
`sh`, `curl`, `git`, a populated CA store, and the `agent` user at uid 1000.
A hook running `curl` or `git` has declared nothing by doing so. Worse,
`requires: ["deb/curl"]` refuses every workload without a dpkg database —
an Alpine or Wolfi base publishes `apk/` names — so the entry rules out bases
that were always going to satisfy it.

**A conditional dependency cannot be expressed here, so do not try.** There is
no either/or in `requires`: an entry is a hard precondition on every
composition. A hook that reaches for `apt-get` *only when the tool it installs
is missing* works fine on a base that already ships the tool, and
`requires: ["deb/apt"]` converts "degrades on some bases" into "refuses on
them". Let the hook probe and fail with an actionable message, and say in a
comment that the silence is deliberate — otherwise the next reader adds the
entry back.

The test is not "what do my hooks run" but **"what must already be present, on
every base, for this kit to work at all"**. Usually that is nothing.

Where a requirement is real, `deb/` names are the right vocabulary and
invented ones are wrong: publishing derives a `deb/<pkg>` provide from a
**workload's** dpkg database, so `requires: ["deb/apt"]`, `["deb/jq"]` or
`["deb/docker-ce"]` resolve against any Debian-based workload. On a
multi-platform workload the derived set is the **intersection** — §9.6 emits a
package only where every published platform agrees on its normalized name and
version — so check each arch, not just your own
(`docker run --rm --platform linux/arm64 <base> dpkg-query -W -f='${Version} ${Status}\n' <pkg>`),
and never *author* a `deb/` provide — the frontend refuses it.

## Content recipes

Recipe patterns for both kinds, including the ownership rules an overlay must
satisfy, are in [RECIPES.md](RECIPES.md). Read it before writing a mixin:
overlay ownership is the single most common way a working-looking kit is
broken.

## Build, run, verify

```sh
# 1. validate — the descriptor is checked before any content is built, so this
#    fails in a second on a bad field. cacheonly drops the EXPORT, not the
#    build: once the descriptor is valid the whole recipe still solves.
cd <kit> && docker buildx build . -f <kit>.yaml --output type=cacheonly

# 2. build, exporting a layout so kit-tck can judge it without a registry
docker buildx build . -f <kit>.yaml -t <kit>:<version> \
  --output type=oci,dest=/tmp/<kit>-layout,tar=false

# 3. conformance — NB the tag alone, not <kit>:<version>
kit-tck kit --layout /tmp/<kit>-layout <version>

# 4. run it, no registry needed
sbx run ./<kit> .                          # a workload
sbx run ./<workload> --kit ./<kit> .       # a mixin, composed
sbx kit inspect ./<kit>                    # resolved declarations, no sandbox
```

Inside a sandbox the kit is self-describing: `/usr/share/sandbox/kit/<kit>/kit.yaml`
is the published descriptor, `kit.dockerfile` the recipe, and
`/var/log/sbx-kit-startup.log` the startup hook output.

**A build is not proof the kit works.** For a mixin especially, compose the
built overlay onto a bare base and run the tool — that is the only check that
catches an overlay shipping a dangling symlink, which happens whenever an
installer relocates a launcher but not its payload and the build-stage
`test -x` passes because the payload is still there. Details and the ownership
audit are in [RECIPES.md](RECIPES.md#verifying-an-overlay).

## Publish

**Publishing is the build.** A kit is an OCI artifact and the frontend has
already written its annotations, staged sources and config, so the thing in
the registry is the kit — there is no pack step, no sidecar artifact and no
`kit push` subcommand to look for. Add `--push` to the build that produced the
kit you verified:

```sh
docker buildx build . -f <kit>.yaml --platform linux/amd64,linux/arm64 --push \
  -t <registry>/<kit>:<version> -t <registry>/<kit>:latest \
  --metadata-file /tmp/<kit>-push.json
```

**Push both platforms in one invocation.** One build writes the index
consumers resolve through; two single-platform builds pushed to the same tag
replace each other, leaving a tag that serves whichever ran last and silently
fails for everyone on the other architecture.

Tag the version and `latest` together. `<version>` is the descriptor's
expanded `version:`, so where a `version` arg drives the install it also names
the tag, and the tag says exactly what the image contains.

**Where several kits share one repository, the version belongs in the tag.**
A repository per kit is the simple case; a repository holding a family of them
distinguishes kits *by tag*, which leaves `<kit>:<version>` nowhere to put the
version. Join them and keep the bare name as the moving tag:

```sh
docker buildx build . -f <kit>.yaml --platform linux/amd64,linux/arm64 --push \
  -t <registry>/<kits-repo>:<kit>-<version> \
  -t <registry>/<kits-repo>:<kit>
```

Publishing only the bare name leaves consumers no way to ask for a particular
build, or to notice they were moved onto a different one. Reference the
immutable tag from anything that has to keep working — and note that a
version-shaped *tag* is also one of the inputs that answers an unversioned
`provides` entry, which `<kit>-<version>` is not. That is a reason to state
`version:` in the descriptor rather than leaning on how you tagged.

Signing is optional and orthogonal. A signature is stored as its own object in
the repository rather than as part of the image, so it changes neither the
kit's digest nor its annotations, and a signed kit's `kit-tck` verdict is the
one it already had:

```sh
digest=$(jq -r '."containerimage.digest"' /tmp/<kit>-push.json)
cosign sign --yes <registry>/<kit>@"$digest"
```

**Sign the digest, never the tag.** `--metadata-file` reports the digest the
push actually produced; a tag is mutable, so a signature naming one attests to
whatever it happened to point at. For a multi-platform build that digest is
the index's, which covers the per-platform manifests beneath it.

In CI, keyless signing avoids managing a key at all: grant the job
`id-token: write` and cosign takes its identity from the OIDC token. The
matching verification names the identity rather than a public key:

```sh
cosign verify <registry>/<kit>@"$digest" \
  --certificate-identity-regexp '^https://github\.com/<org>/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Tooling

- **`docker buildx`** — nothing to install for the frontend; BuildKit pulls
  `docker/sandbox-kit:3` from the `# syntax=` line.
- **`sbx`** — kit v3 needs a release candidate or nightly from
  [sbx-releases](https://github.com/docker/sbx-releases), not the stable line.
  Note `sbx kit validate` does **not** accept a v3 source kit; `sbx kit inspect`
  does.
- **`kit-tck`** — `go install github.com/docker/sandbox-kit-spec/v3/cmd/kit-tck@latest`.

  *While the repository is private* this also needs access to it plus
  `GOPRIVATE=github.com/docker/*`; without access you can still validate by
  building. Delete this paragraph when the repository goes public — nothing
  else here depends on it.
- **`cosign`** — only if you sign. Nothing in the kit grammar requires it and
  no consumer needs it to run a kit.

## Reference

- [SPEC-v3.md](https://github.com/docker/sandbox-kit-spec/blob/main/docs/spec/SPEC-v3.md)
  — the grammar. §3 authoring forms, §5 provides/requires, §6 args,
  §7 capabilities, §9.6 derived provides.
- [capability pages](https://github.com/docker/sandbox-kit-spec/tree/main/docs/spec/capabilities/com.docker.sandbox)
  — normative per type.
- [examples](https://github.com/docker/sandbox-kit-spec/tree/main/examples) —
  `gh` for a self-contained tool mixin, `hello` for the smallest workload,
  `claude` and `claude-mixin` for one agent in both shapes, `motd` for the
  single-file inline form, `team` for a set.
- Where the docs and the Go implementation in `spec/` disagree, the code wins.
