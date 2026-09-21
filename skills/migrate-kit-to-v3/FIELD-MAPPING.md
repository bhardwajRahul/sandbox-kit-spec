# v2 → v3 field mapping

Reference for [SKILL.md](SKILL.md) step 2. The normative grammar is
[SPEC-v3.md](https://github.com/docker/sandbox-kit-spec/blob/main/docs/spec/SPEC-v3.md);
each capability's config schema and runtime behavior is normative on its own
[capability page](https://github.com/docker/sandbox-kit-spec/tree/main/docs/spec/capabilities/com.docker.sandbox).
Both are also on disk under `docs/spec/` when working inside the spec repo, and
the worked kits referenced below are under
[examples](https://github.com/docker/sandbox-kit-spec/tree/main/examples).

## Top-level fields

```text
schemaVersion: "2"            →  schemaVersion: "3"
name: claude                  →  (dropped — identity is the consumption reference)
kind: sandbox                 →  kind: workload      # never write "sandbox"
kind: mixin                   →  kind: mixin
displayName / description     →  unchanged
version: "1.0.0"              →  unchanged (fallback version for unversioned provides)
licenses: [...]               →  unchanged
sourceURL: ...                →  sourceUrl: ...
requires: {agent: claude}     →  requires: ["claude"]
args: {...}                   →  same shape, plus `env:` or `buildArg:` (see below)
sandbox.image: IMG            →  the recipe's `FROM IMG`
sandbox.entrypoint: [...]     →  `ENTRYPOINT [...]` in the recipe
sandbox.command.default       →  `CMD [...]` in the recipe
sandbox.command.interactive   →  capability lifecycle@1 `interactive:`
environment.variables         →  `ENV` in the recipe (workload only — see mixins)
permissions.network           →  capability network-policy@1
credentials[]                 →  capability credential@1, one per service
volumes[]                     →  capability volume@1, one per path
ports[]                       →  capability port@1, one per port
setup.install/startup/files   →  capability lifecycle@1
agentInstructions             →  capability agent-context@1
```

Every key in v3 is `lowerCamelCase` with acronyms title-cased: `sourceUrl`,
`apiKey`, `iconUrl`. Decoding is strict — an unrecognized key anywhere is an
error, which is why a v2 `name:` left in place fails rather than being ignored.

The image config owns the runtime contract in v3. Entrypoint, cmd, env, user and
workdir move out of the descriptor and into the recipe; the descriptor
duplicates none of it. The one exception is the interactive argv tail, which has
no image-config slot and lives in `lifecycle@1.interactive`.

Because the entrypoint moves into the image config, **a v2 `Dockerfile`'s own
`CMD` usually has to go**. v2 recipes commonly carried a `CMD` mirroring the
kit's entrypoint so a plain `docker run` behaved like the sandbox; left in place
beside the migrated `ENTRYPOINT`, that `CMD` becomes a stray trailing argument
in the headless argv (`Entrypoint` + `Cmd`). Keep a `CMD` only where it carries
default *arguments* to the entrypoint's binary — which is exactly what
`sandbox.command.default` was.

### Kits with no v2 `Dockerfile`

Several v2 sandbox kits point `sandbox.image` at a published template and ship
no recipe. A v3 workload MUST have content, so write the minimal recipe that
says the same thing: `FROM <that image>`, plus the `ENV` from
`environment.variables` and the `ENTRYPOINT` from `sandbox.entrypoint`.

### Args

Same shape as v2 (`default`, `required`, `description`, `enum`, `pattern`), with
two additions that decide when the value resolves:

- `buildArg: FOO_VERSION` — resolves at **build**, is handed to the recipe as
  `--build-arg FOO_VERSION=<value>`, and every reference to it is expanded into
  the published descriptor.
- `env: FOO_TIMEOUT` — resolves at **create** and is exported to the container
  under that name.

They are mutually exclusive: an arg resolves in one phase. Args are private by
default — an arg reaches the container only through `env:` and the build only
through `buildArg:`.

References change spelling: a v2 `${argname}` substitution becomes
`${{ kit.args.argname }}`. Shell variables (`$VAR`, `${VAR}`) pass through
untouched, which is the whole point of the `${{` opener — it is not valid shell,
so the two vocabularies cannot collide. Every `${{ kit.args.x }}` must name a
declared arg.

**That applies inside comments too.** References are found by scanning the
descriptor's raw text, so a placeholder written in YAML prose is a real
reference and fails validation when the arg does not exist. Name the arg
(“the `version` arg”) rather than spelling the placeholder when documenting.

A whole-value reference adopts its type, so a numeric arg can reach a numeric
field (`container: ${{ kit.args.port }}`). A reference embedded in a larger
string is always text.

### Provides

- Where the recipe pins the tool's version through a build arg, declare the arg
  and reference it: `provides: ["<tool>@${{ kit.args.version }}"]`.
- Where the install floats, use an unversioned `provides: ["<tool>"]` and keep a
  `version:` fallback. At publish every provide must carry a version — its own
  or the descriptor's `version:`.
- **Know what an unversioned provide resolves to, because it is rarely what you
  want.** The resolver takes, in order: an explicit `@version` on the provide;
  the version a *version-shaped consumption reference* carries; then the
  descriptor's `version:`. A `:latest` tag is not version-shaped, so it
  contributes nothing. That means a kit shipping Claude Code 2.1.267 under
  `version: "1.0.0"` offers `claude@1.0.0`, and a mixin asking for
  `claude >= 2.1` refuses to resolve against the very kit that satisfies it.
  Unversioned is only honest when the kit genuinely cannot know its tool's
  version.
- A version is `[0-9]+(\.[0-9A-Za-z-]+)*`: the first segment must be numeric,
  later segments may be alphanumeric, and there is no `v` prefix. **A commit SHA
  is therefore not a version**, so a kit pinned to a git ref cannot reference
  that pin into its provide. Use the upstream version the recipe records if
  there is one, else leave the provide unversioned — never reach for a
  `version: "1.0.0"` fallback that would publish `<tool>@1.0.0`, which is the
  kit's release number wearing the tool's name.
- **State the version once.** Where a kit pins its tool through a build-phase
  arg, point the top-level field at the same arg — `version: "${{ kit.args.version }}"`,
  which §4 allows and publishing expands. One input then drives the descriptor's
  `version`, its `provides` entry, the `org.opencontainers.image.version`
  annotation and the publish tag, with no second place to drift.
- **A pinned provide is a claim about content, so make the build enforce it.**
  Wire the arg through to the installer and add a step that re-reads the
  installed version and fails on mismatch. Pinning the provide without pinning
  the install is worse than floating: it asserts a version the content may not
  have. Where the tool arrives inside a base image rather than from an install
  the kit performs, the honest form is an *assertion* — read the version out of
  the content and fail the build when it differs from the declared default.
- **Some installers genuinely cannot be pinned**, and leaving those unversioned
  with the evidence recorded is the right answer. Real examples: an installer
  whose whole option surface is `--help` and `--channel` and which fetches a
  literal `latest/` path; a vendor manifest whose asset URL carries an opaque
  build id beside the version; a tool that self-updates at run time; content
  that is someone else's mutable image tag.
- The name must match what v2 mixins asked for via `requires: {agent: X}`, so
  those requires keep resolving.

### `requires` deserves more than the v2 agent affinity

v2 could express exactly one dependency — `requires: {agent: <name>}` — so a v2
kit whose hook runs `apt-get`, calls `jq`, or drives the in-sandbox Docker
daemon says nothing about needing them, and simply fails at create on a base
that lacks them. Migrating only the agent affinity carries that silence
forward. Audit what each kit assumes **beyond the platform floor** (§12
guarantees only `bash`, `sh`, `curl`, `git`, a CA store, and the `agent` user at
uid 1000) and declare the rest.

What makes this expressible is **derived provides** (§9.6): publishing a
`kind: workload` kit reads `/var/lib/dpkg/status` (or apk's database) out of the
built content and states one provide per installed package under `deb/` or
`apk/`, at the upstream version with epochs and Debian revisions stripped. So a
workload on `docker/sandbox-templates:*` offers `deb/apt`, `deb/jq`,
`deb/docker-ce`, `deb/git` and hundreds more with nobody authoring them, and a
mixin may require those names:

```yaml
requires: ["claude", "deb/apt", "deb/openssl >= 3.5"]
```

Three things to keep straight:

- **Requiring a `deb/` name is fine; authoring one as a provide is refused.**
  `provides: ["deb/apt"]` fails the build — §5.1 reserves the namespace for
  publishing to fill — and that check is authored-form only, so
  `spec.ValidateRaw` does not catch it and only the build tells you.
- **Verify the package is really in the database.** A tilde in a version's
  upstream half makes §9.6 drop the package rather than publish it, so
  `deb/nodejs` does not exist on these templates even though `nodejs` is
  installed — and they put node in `/usr/local` via `n`, outside dpkg entirely.
  Check with
  `docker run --rm <template> dpkg-query -W -f='${Version} ${Status}\n' <pkg>`.
- **An unprovidable name is worse than silence.** `requires` is a closed-set
  check (§5.3): a name nothing in the set provides makes the kit refuse to
  compose anywhere, rather than only on the bases that actually lack it. That is
  why an invented `docker-engine` is wrong and `deb/docker-ce` is right.

## Capabilities

Each entry is a typed request the host answers. `optional: true` means the kit
degrades gracefully without it; the default is required, which fails resolution
closed. Policy-shaped types are singletons; instance-shaped types appear once
per thing requested (`credential@1` per (service, phase), `volume@1` per path,
`port@1` per (container, transport), `agent-skills@1` per path).

### network-policy@1

v2 had one flat list. v3 is phase-scoped — `install` and `runtime` — and an
absent phase grants nothing. Split the v2 list by who reaches the host:

- reached by `setup.install` hooks → `install.allow`
- reached by the agent in steady state, or by `setup.startup` hooks (they run at
  boot, in the runtime phase) → `runtime.allow`
- reached by both → list in both

The v2 comments usually say which hook needs which host. The union of the two
phases must not lose a host. Deny wins over allow, and a removed deny is a
widening. `network-policy@2` adds HTTP method and path rules over the same
grants and is exclusive with `@1`; a kit that gates by host alone stays on `@1`.

Write entries portless (`archive.ubuntu.com`, not `archive.ubuntu.com:80`). A
portless pattern matches any port, which is what apt hosts need — pinning `:80`
breaks the moment a mirror answers over HTTPS, and `apt-get update` then fails
wholesale rather than skipping one source. For a v2 kit that did pin ports this
is a deliberate widening, so note it in the migration's review notes rather than
letting it pass as a transcription detail.

### credential@1

One entry per v2 `credentials[]` element, with `service:` and `phase:`
(`runtime` unless the credential exists only so install hooks can download
something). `apiKey`, `inject`, `proxyManaged`, `oauth.tokenEndpoint`,
`sentinels`, `resourceHosts`, `responseFields` and `passthrough` carry over
unchanged.

- **Add `optional: true` to preserve v2 behavior.** v2 credentials default to
  not-required; v3 entries are required unless they opt out.
- `oauth.skipIfEnv` is **dropped**: mode resolution is binding-driven in v3, not
  an env-var probe inside the container. Note it as a `MIGRATION NOTE`.
- `credentialFile.template` (a Go template) becomes the declarative
  `credentialFile.structure` map, encoded after substitution so the output is
  well-formed whatever the values contain. The placeholder vocabulary is
  `{{.AccessToken}}`, `{{.RefreshToken}}`, `{{.ExpiresAt}}` (a number),
  `{{.Scopes}}` (an array) and `{{.PrimaryApiKey}}` (whose enclosing key is
  omitted when no key is captured). `format: toml` is available for agents that
  read TOML.
- Validation enforces that every `inject[].domain` appears in the **same
  phase's** allow list. An inject domain outside it would be a credential mapped
  onto a connection that can never occur — which is how the claude migration
  found two dead inject rules.
- That check is an **exact host match**, escaped only by a bare `*` or `**`. A
  single-label wildcard like `*.example.com` does not satisfy an inject domain
  of `api.example.com`, even though it covers the host at run time, because the
  matcher does not expand globs. Add the literal host beside the wildcard; it
  widens nothing.

### lifecycle@1

One entry holding `install:`, `startup:`, `files:` and `interactive:`. Commands,
`user:` and `description:` carry over verbatim.

**First ask whether the hook should exist at all.** In v2 a mixin had no content
mechanism, so an install hook was the only way for one to install anything. v3
lifts that — a mixin may carry an overlay — so many install hooks encode a v2
limitation rather than a v3 requirement, and a faithful migration carries them
across intact when they should become layers. Moving a pure-content install
into the recipe buys four things: no per-sandbox-create latency, digest-pinned
and scannable content, a failure that surfaces at publish instead of in a
user's sandbox, and — the one that shows up in the permission surface — the
install-phase network grant disappears, because it existed only to let that
hook reach the network.

Move it when it is a pinned artifact download or a language-package install
that needs nothing from create time. Keep it a hook when:

- it runs `apt-get` **in a mixin** — apt needs the composed base's real dpkg
  database and keyring, and an overlay cannot carry a package's shared-library
  closure;
- it reads a create-time value (`WORKSPACE_DIR`, `SBX_CRED_*_MODE`,
  `MCP_GATEWAY_URL`, or an arg that resolves at create);
- it needs the in-sandbox Docker daemon, which does not exist at build;
- its target path is a declared `volume@1`, or an `agent-skills@1` path the
  host mounts over — baking there ships content the mount then hides;
- it merges into a file the composed base also writes. A layer *replaces* a
  file rather than merging it, so a shared registry or a `~/.bashrc` line has
  to be appended at create.

- **Hook environments are deny-by-default.** Every `$VAR` a hook body reads must
  be listed in that hook's `env: [...]`, except the platform baseline (`PATH`,
  `HOME`, `HOSTNAME`, `TERM`, `PWD`, `OLDPWD`, `SHLVL`, `_`). This is the single
  most common migration bug — audit every hook body.
- **A hook's children need their variables declared too.** The restriction is on
  the hook's whole environment, not on the names its script spells out, so a
  hook that never mentions `$HTTPS_PROXY` but shells out to `curl`, `pip`, `npm`
  or `docker` still has to declare what those read — `HTTP_PROXY`/`HTTPS_PROXY`
  for anything fetching through the sandbox's forced proxy, `DOCKER_HOST` where
  a base points the CLI at a non-default socket. v2's unrestricted hook
  environment handed these over silently, so a faithful migration declares them
  and says why. `examples/builder/builder.yaml` declares the proxy pair for
  exactly this reason.
- `files[].onlyIfMissing: true` → `overwrite: false`.
- `files[].content` expands `${{ kit.args.* }}` and nothing else; it gets no
  runtime variable substitution. A v2 file whose content relied on a runtime
  `${WORKDIR}` substitution must either keep the reference inside a script the
  shell expands when it runs, or become an install hook that writes the file and
  declares `env: [WORKSPACE_DIR]`. Prefer the hook. The variable is spelled
  `WORKSPACE_DIR` in v3 hooks.
- A v2 `... &` backgrounding trick becomes `background: true`.
- Install hooks run once at create, before the entrypoint, with the install-phase
  network and credentials open. Startup hooks run on every boot and must be
  idempotent.

### agent-context@1

Move the v2 `agentInstructions.content` body verbatim into `<kit>-context.md`
and reference it with `contentFile: ./<kit>-context.md`; the frontend stages the
body into the image and rewrites the field to the staged path.

**Unless the body interpolates an arg — then use inline `content:`.** A
`contentFile` body is read and staged into a layer at publish, while
create-phase args expand into the *descriptor* at create, so a
`${{ kit.args.host }}` inside a staged body is never expanded: it is staged
byte-for-byte and reaches the agent as literal placeholder text, with no
warning from the build. Inline `content:` is a descriptor string and does
expand. This is the one case where a kit's instructions name the instance, the
clone directory, or anything else an installer chooses.

Audit for the mistake with:

```sh
rg -l '\$\{\{ *kit\.args\.' --glob '*-context.md' .
```

`filename:` (`CLAUDE.md`, `AGENTS.md`, …) is **workload-only** — the profile
belongs to the kit that owns the environment, and declaring it on a mixin is a
validation error. A mixin carries `contentFile` alone.

### sbx@1

Config-less. Add it to every migrated **workload**: v2 `kind: sandbox` kits are
all sbx-launched agents, and this is what asks the host to launch the agent
rather than let the image entrypoint become PID 1, and to honor the identity the
image config declares. Never on a mixin — a mixin's image config does not become
the composed image's, so the declaration would say nothing a host can act on.

The kit side of the claim is real: the image must ship `/bin/sh` and `/bin/bash`
the declared user can execute, declare a non-empty `user` that resolves in
`/etc/passwd`, and name an absolute shipped file in `BASH_ENV`.

### port@1

One entry per v2 port. `name:` and `container:` carry over; v2 `protocol: tcp`
becomes `transport: tcp`. A kit cannot pin a host port.

### agent-sessions@1

Workload-only, and only where the v2 kit's `testdata/tck.yaml` recorded a
working non-interactive invocation in `promptArgs`. Translate it into the verb
tails: `promptArgs: ["-p"]` → `prompt: ["-p", "{{.Prompt}}"]`, plus `continue`,
`resume` and `list` where the agent supports them. Where `promptArgs` was
deliberately omitted, omit the capability — do not invent flags.

### agent-skills@1

Only where the v2 kit already declares or documents the agent's skills
directory. Do not invent a path. `mode` defaults to `readonly`, and the
effective access is the narrower of the host's setting and the kit's.

A kit that *names* a skills directory still does not necessarily want this
capability: it asks the host to **mount** its store at that path, so where the
path holds content the kit itself ships — a skill pack baked into the image,
registered command symlinks — the mount would shadow exactly what the kit
exists to deliver. Declare it for a directory the agent reads from, not for one
the kit writes to.

### Do not add on speculation

`privileged@1`, `resources@1`, `kit-registry@1` and `usb-device@1` belong in a
migrated kit only if the v2 kit declared the equivalent. A v2 Dockerfile's
`LABEL com.docker.sandboxes.start-docker="true"` stays a label in the recipe —
it is not a v3 capability.

## Mixin variants

A workload kit's `-mixin` sibling declares the same credentials, network policy,
volumes, hooks, args and provides, minus what only the kit that owns the
environment can carry:

- no `sbx@1`, no `agent-sessions@1`, no `agent-context@1.filename`
  (`contentFile` only)
- no `ENTRYPOINT` — the base workload's launch command stays, and the user runs
  the tool from the shell. Say so in the descriptor's header comment.
- `displayName: <Name> (mixin)`, and a description that says to layer it onto a
  shell base and run the tool.

v2 `environment.variables` cannot become `ENV` in a mixin, because a mixin's
image config is not the composed image's. Drop the exports into
`/etc/profile.d/<kit>-env.sh` in the overlay instead, which the base's login
shell sources. `examples/cursor-mixin/` and `examples/codex-mixin/` show the
shape.

The recipe is an overlay, not a root filesystem:

```dockerfile
# syntax=docker/dockerfile:1
FROM <the workload's base> AS build
# the same install the workload does, landed under /out
RUN ...
RUN mkdir -p /out/etc/profile.d && printf 'export IS_SANDBOX=1\n' > /out/etc/profile.d/<kit>-env.sh

# The overlay: lands on any base.
FROM scratch
COPY --from=build /out /
```

Where an install genuinely cannot be relocated — apt packages, `uv tool
install`, npm global installs — use the workload's own base as a build stage,
run the unmodified install, then `COPY --from=build` the specific resulting
paths (`/usr/local/bin/...`, `/home/agent/.local/...`, `/opt/...`) into the
`scratch` overlay, and comment on why that shape was chosen.

**An overlay must reproduce the base's ownership at every level it ships**, and
it is easy to get wrong in both directions, because an overlay's directory
entries override the base's:

- `/home` owned by uid 1000 hands the agent a directory it should not own.
- `/home/agent` owned by root takes `$HOME` away from the agent user, and the
  entrypoint runs as that user, so its own `chown` would be a no-op.

`COPY --chown=1000:1000 … /home/agent/x` straight into `scratch` produces the
first, because BuildKit applies the `--chown` to every parent it creates.
A `chown -R` over the whole staging tree produces it too. A `chown -R` one
level too deep produces the second.

The idiom that gets both right stages under `/out` and starts the chown exactly
at the agent's home:

```dockerfile
USER root
RUN mkdir -p /out/home/agent \
 && cp -a /home/agent/.local /out/home/agent/.local \
 && chown -R 1000:1000 /out/home/agent

FROM scratch
COPY --from=build /out /
```

Numeric ownership because `scratch` carries no `/etc/passwd` for a name to
resolve against. Cleanest of all is to avoid the agent's home entirely and
stage into `/usr/local`, `/opt` and `/etc/profile.d`, which is what a mixin
landing on an unknown base should prefer anyway — whatever is at
`/home/agent` may be a mounted volume.

**Foreign owners ride along in package trees.** npm and PyPI tarballs preserve
whatever uid the publisher's machine had, and `cp -a` carries it into the
overlay — real examples from these kits are `501:20` (a macOS developer),
`1001:127` (a CI runner) and `718322462:454177323`. A create-time hook made
those harmless, because the install ran against the real base and its own
`--global` prefix; as **image content on an unknown base** they may be real
accounts, and a file's owner can rewrite it whatever its mode says. Normalize
a copied package tree to root (`chown -R 0:0`), which is how a root-installed
global package looks anyway. The agent needs write access to the *prefix
directories* to add packages, not to the tool's own tree.

**Verify it rather than reasoning about it.** Export the layer and read the
ownership out:

```sh
docker buildx build . -f <kit>.yaml --output type=oci,dest=/tmp/layout,tar=false
for b in /tmp/layout/blobs/sha256/*; do tar tvf "$b" 2>/dev/null; done \
  | awk '{print $3":"$4}' | sort | uniq -c | sort -rn
```

Every count should be under `0:0` or `1000:1000`, and nothing else. To see the
directory invariant specifically, filter for `^d.*home`: `home/` must be `0 0`
and `home/agent/` must be `1000 1000`.
`examples/devin-mixin/` and `examples/docker-agent-mixin/` show the copy-out
pattern; `examples/claude-mixin/` shows the single-binary case.

**A virtualenv travels only if its interpreter travels with it.** A venv keeps
packages in `lib/python3.<minor>` and points `bin/python` at an absolute path;
left pointing at the build base's `/usr/bin/python3`, the copied tree resolves
the *composed* base's python and fails with `ModuleNotFoundError` — after the
launcher starts, so create succeeds and use breaks. Either bundle the
interpreter (`uv tool install --managed-python --python <minor>` puts a
standalone CPython inside the tree the overlay already copies) or leave the
install a create-time hook. `readlink -f <venv>/bin/python` is the tell: inside
the overlay's own tree is self-contained, `/usr/bin/python3` is not — and the
bug hides whenever the build and test bases share a minor version.

**Watch for installers that relocate a launcher but not the tree.** A
`--prefix`, `--dir` or `INSTALL_DIR` option frequently moves only the symlink
while the real payload lands somewhere else — `$CODEX_HOME`, `~/.grok/downloads`
— and a `test -x` gate on the launcher passes *in the build stage* because the
payload is still sitting behind it there. The overlay then ships a dangling
link and the tool cannot run on any base. Three kits shipped this way before it
was caught. Gate on the resolved binary, and prove the overlay by composing it
onto a bare base and running the tool, not by building it.

A kit's build context is rooted at its own directory and a `dockerfile:` path
may not escape it, so a mixin **cannot** reach an asset sitting in the
workload's directory. A shipped script the two shapes share has to be copied
into both, and the copies then have to move together — say so in a comment in
each, because nothing enforces it.

## Gotcha checklist

Before calling a migration done:

- [ ] no `name:` field, and `kind:` says `workload`, never `sandbox`
- [ ] every hook that reads a variable declares it in `env:`
- [ ] every credential that was effectively optional in v2 sets `optional: true`
- [ ] every inject domain appears in the same phase's allow list
- [ ] the install/runtime phase split loses no host from the v2 list
- [ ] `filename:` appears on the workload only
- [ ] no `sbx@1` or `agent-sessions@1` on a mixin
- [ ] the mixin's env arrives via `/etc/profile.d`, not `ENV`
- [ ] entrypoint, env, user and workdir live in the recipe, not the descriptor
- [ ] every volume declares a `size`
- [ ] v2 comments carried across, deltas marked `# MIGRATION NOTE:`
- [ ] every `contentFile:` names a file that exists, and every staged
      `*-context.md` is referenced by a descriptor
- [ ] a v2 `agentInstructions.content` body still reaches the agent, as a
      staged file or inline `content:` — a dropped body breaks nothing and is
      invisible
- [ ] the recipe's `ENTRYPOINT` (plus any `CMD`) reproduces the v2
      `sandbox.entrypoint` and `sandbox.command.default` exactly

The last one is worth scripting, because descriptor validation cannot see it —
a `contentFile` pointing at a missing file fails at build, and a context body
nobody references fails at nothing at all and silently never reaches the agent:

```sh
for y in */*.yaml; do d=$(dirname "$y")
  rg -o 'contentFile: *\./[^ ]+' "$y" | sed 's|contentFile: *\./||' | while read -r f; do
    [ -f "$d/$f" ] || echo "MISSING: $y -> $f"
  done
done
for m in */*-context.md; do d=$(dirname "$m"); b=$(basename "$m")
  rg -q "contentFile: *\./$b" "$d"/*.yaml || echo "ORPHAN: $m"
done
```
