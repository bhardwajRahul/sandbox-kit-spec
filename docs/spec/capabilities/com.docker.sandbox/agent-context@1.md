# `com.docker.sandbox/agent-context@1`

Written instruction content the agent reads — the AGENTS.md family. A host
with no such concept skips an optional declaration or refuses a required
one.

- **Shape**: singleton — at most one entry per descriptor.
- **Permission surface**: **no** — instruction text the agent reads, on the
  entrypoint's trust plane.

## Config

```yaml
# A workload kit: owns the profile, ships its body as a staged file.
- type: com.docker.sandbox/agent-context@1
  config:
    filename: CLAUDE.md              # workload-only: the profile this kit owns
    contentFile: ./CLAUDE-context.md # authored path; staged + rewritten at publish

# A mixin: contributes content, owns no profile.
- type: com.docker.sandbox/agent-context@1
  config:
    contentFile: ./gh-context.md

# A content-free kit: small instructions inline.
- type: com.docker.sandbox/agent-context@1
  config:
    content: |
      Use `motd` to inspect the message of the day.
```

| Field | Type | Rules |
|---|---|---|
| `filename` | string | The context-file profile the agent reads (`CLAUDE.md`, `AGENTS.md`, …). **Workload Kits only** — the profile belongs to the Kit that owns the environment; declaring it on a mixin is an error. |
| `contentFile` | string | Path to the context body. Authored as a path relative to the build context; the frontend stages the body into the image under `/usr/share/sandbox/kit/<stem>/` and **rewrites this field to the staged in-image path** in the published descriptor. Mutually exclusive with `content`. |
| `content` | string | The body inline, for content-free Kits with no layers to stage into. Mutually exclusive with `contentFile`. |

## Publish behavior

When `contentFile` is set, the frontend reads the authored file, stages it
into the Kit's image filesystem, and publishes the descriptor with
`contentFile` pointing at the staged path. The published artifact is
self-contained: consumers never resolve authored-relative paths.

## Runtime behavior

A conforming runtime:

- **MUST** treat the workload Kit's `filename` as the profile file it <!-- tck: agent-context@1/workload-filename-is-profile -->
  materializes for the agent, seeded with the runtime's own guidance. It
  belongs **beside** the workspace — the workspace directory's sibling,
  not a file inside it: a workspace is usually the user's own checkout,
  and a profile written into it would arrive as an untracked change to
  their tree.
- **MUST** surface each contributing Kit's context **progressively**: the <!-- tck: agent-context@1/progressive-surfacing -->
  profile carries a per-kit index (a "Kits" section) telling the agent
  which Kit contributed what and where to read it on demand — stacking
  mixins does not bloat the always-loaded profile.
- For **staged** content (`contentFile`, published form): the body already
  sits in the assembled image's filesystem, so the runtime **points** the
  agent at the staged path from the index. It does not copy the body.
- For **inline** content (`content`): the runtime writes a per-kit file
  (under a directory beside the profile) and points the index at it.
- **MUST NOT** treat context content as trusted input to the runtime <!-- tck: agent-context@1/content-untrusted -->
  itself: it is prose for the agent. Runtimes SHOULD neutralize any <!-- tck: agent-context@1/content-neutralized -->
  index-management sentinels appearing in kit-supplied text.

## Composition

The workload's entry provides the profile (`filename`) and its own body;
each mixin contributes one body. Per-kit attribution survives composition —
the index lists Kits individually, in composition order.
