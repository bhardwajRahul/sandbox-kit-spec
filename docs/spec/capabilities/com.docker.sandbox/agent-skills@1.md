# `com.docker.sandbox/agent-skills@1`

The host's shared agent-skills store, at the path this kit's agent reads
skills from.

- **Shape**: instance — one entry per path.
- **Permission surface**: yes — the path, and write access separately
  when `mode` is `readwrite`.

## Config

```yaml
- type: com.docker.sandbox/agent-skills@1
  config:
    path: /home/agent/.claude/skills   # REQUIRED, absolute
    mode: readonly                     # optional: "readonly" (default) | "readwrite"
```

| Field | Type | Rules |
|---|---|---|
| `path` | string | REQUIRED. Absolute, canonical in-container path where the agent reads skills: no `.` or `..` segments, no trailing slash, not `/` itself. An alias such as `/x/../skills` for a declared `/skills` would evade the duplicate check, so canonical form is validated rather than normalized in. |
| `mode` | string | optional. `readonly` (default) or `readwrite`. The most access the kit is willing to take, not a demand. |

Two entries naming one path are rejected: identical ones as a duplicate
request, differing ones as a contradiction about the same mount.

## Access

Both sides bound the result, and neither can exceed the other. The host
decides how much access it is prepared to give, and the kit declares how
much it is prepared to take:

| Host setting | Kit `mode` | Effective |
|---|---|---|
| off | anything | no mount |
| readonly | `readwrite` | read-only — the host withholds write |
| readwrite | `readonly` (or unset) | read-only — the kit never asked for write |
| readwrite | `readwrite` | read-write |

The kit's half matters as much as the host's. An agent that only reads
skills says so, and then a permissive host does not hand it the ability to
rewrite the user's shared store — which is why an omitted mode means
read-only rather than "whatever the host allows".

## Why the kit declares the path

A runtime cannot know where an arbitrary agent reads skills. It can know
for the agents it ships, and a runtime **MAY** keep such a mapping for
them, but a kit that runs an agent behind a wrapper — or one the runtime
has never heard of — reads from a path no host-side table predicts. The
declaration is what lets the store reach those kits at all.

It also composes. A sandbox built from a shell workload plus two agent
mixins has two skills paths, one per mixin, which a single sandbox-wide
agent identity cannot express.

## Runtime behavior

A conforming runtime:

- **MUST** mount the shared skills store at `path` when the store exists <!-- tck: agent-skills@1/store-mounted-at-declared-path -->
  and the host's skills setting is not off.
- **MUST** mount it before lifecycle hooks run, so an install hook can <!-- tck: agent-skills@1/mounted-before-hooks -->
  read what the user shared.
- **MUST** resolve access as the narrower of the host's setting and the <!-- tck: agent-skills@1/access-narrower-of-both -->
  kit's `mode`, and **MUST NOT** exceed either. A host that withholds the
  mount withholds it; a kit that asks for `readonly` gets read-only however
  permissive the host is.
- **MUST** refuse a **required** entry it cannot satisfy, rather than <!-- tck: agent-skills@1/host-off-refuses-required -->
  starting the sandbox without the mount. A kit declaring this needs it;
  an optional entry is the way to say otherwise.
- **MUST** default an omitted `mode` to `readonly`. Skills are input to <!-- tck: agent-skills@1/readonly-default-honored -->
  the agent, and a sandbox that can rewrite the user's shared store affects
  every later sandbox, so write access is something both sides opt into.
- **MUST NOT** treat the store's contents as trusted input to the runtime <!-- tck: agent-skills@1/store-untrusted -->
  itself. Like [agent-context](agent-context@1.md), this is material for
  the agent, not instructions for the host.

What the store contains, where it lives on the host, and how a user fills
it are runtime concerns outside this specification.

## Composition

Paths union across the set, and every declared path receives the same
store. Unlike [volume@1](volume@1.md), two kits naming one path is **not**
a conflict: they are asking for the same content in the same place, which
is satisfied once.

When two kits name one path with different modes, the composition resolves
to the **widest declared mode**, still bounded by the host. A single mount
cannot be read-only and writable at once, and the narrower declaration is
not an isolation boundary that widening would breach: a sandbox is one
filesystem and one process tree, so a kit that declared `readonly` was
never protected from a mount another kit legitimately obtained. `mode`
states what one kit asks for; the union is what the composition asks for,
which is exactly what the gate shows — the merged request surfaces the
write grant, so raising a path to `readwrite` by composing is a widening
the user approves, never a silent escalation. Within a **single** kit the
same situation is a contradiction and is rejected by validation.

## Gate

The path is permission surface, under its own `skills` category rather
than `storage`. The two grant different things — a volume is space the
sandbox is given, while this hands a kit a host directory the user
populated — and a gate naming both the same would not say which was
gained.

A new path widens, and so does raising an existing path from `readonly` to
`readwrite`: reading the user's shared skills and being able to rewrite
them for every later sandbox are different grants. Write is the larger
grant and includes read, so a writable request surfaces as both entries
and giving write up is not a widening.
