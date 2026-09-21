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
| Owns | entrypoint, env, user, workdir, the agent-context `filename` | nothing of the environment |
| Must have content | yes | no — may be declaration-only |

Write a **workload** when you own the environment the agent runs in. Write a
**mixin** when you add a tool, a credential or a policy to somebody else's.
Most new kits should be mixins, and a tool worth shipping as a workload is
usually worth shipping as both — that is what the `claude`/`claude-mixin` pair
in the examples is.

A mixin's image config is **not** the composed image's, so a mixin cannot set
`ENTRYPOINT`, `ENV`, `USER` or `WORKDIR` and have it take effect. Static
environment rides an `/etc/profile.d/<kit>-env.sh` drop in the overlay instead.

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
requires: ["deb/apt"]        # only what you genuinely need; see below

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
| `network-policy@1` | egress | It is **phase-scoped**: an absent phase grants nothing. Hosts your install hooks reach go in `install`, hosts the running agent (or a startup hook) reaches go in `runtime`, hosts both reach go in both. |
| `credential@1` | one service's auth | Entries are **required by default** — add `optional: true` unless the kit genuinely cannot run unauthenticated. Every `inject[].domain` must appear in the same phase's allow list, matched **exactly**: a `*.example.com` wildcard does not satisfy `api.example.com`. |
| `lifecycle@1` | install/startup hooks, staged files | Hook environments are **deny-by-default**. Declare every variable in `env:`, including ones only a child process reads — `curl`, `pip` and `npm` need `HTTP_PROXY`/`HTTPS_PROXY`, and `docker` needs `DOCKER_HOST`. |
| `volume@1` | persistent paths | Always set `size`. An unsized block volume inherits a 50 GiB default whose ext4 inode tables cost ~800 MiB while empty. |
| `agent-context@1` | instructions the agent reads | `filename:` is **workload-only**. Use `contentFile:` for a static body, but inline `content:` when the body interpolates an arg — a staged body is never arg-expanded. |
| `sbx@1` | "launch this as an agent" | Workload-only, config-less. A mixin declaring it says nothing a host can act on. |
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
saying nothing. That is why invented names are wrong and `deb/` names are
right — publishing derives a `deb/<pkg>` provide for every package in a
**workload's** dpkg database, so `requires: ["deb/apt"]`, `["deb/jq"]` or
`["deb/docker-ce"]` resolve against any Debian-based workload. Verify the
package really is installed (`docker run --rm <base> dpkg-query -W -f='${Version} ${Status}\n' <pkg>`),
and never *author* a `deb/` provide — the frontend refuses it.

## Content recipes

Recipe patterns for both kinds, including the ownership rules an overlay must
satisfy, are in [RECIPES.md](RECIPES.md). Read it before writing a mixin:
overlay ownership is the single most common way a working-looking kit is
broken.

## Build, run, verify

```sh
# 1. validate — the frontend validates the descriptor before building content
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

## Tooling

- **`docker buildx`** — nothing to install for the frontend; BuildKit pulls
  `docker/sandbox-kit:3` from the `# syntax=` line.
- **`sbx`** — kit v3 needs a release candidate or nightly from
  [sbx-releases](https://github.com/docker/sbx-releases), not the stable line.
  Note `sbx kit validate` does **not** accept a v3 source kit; `sbx kit inspect`
  does.
- **`kit-tck`** — `go install github.com/docker/sandbox-kit-spec/v3/cmd/kit-tck@latest`.
  That repository is currently private, so this needs access to it plus
  `GOPRIVATE=github.com/docker/*`; without access you can still validate by
  building.

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
