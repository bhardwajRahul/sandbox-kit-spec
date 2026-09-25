# Conformance

This document specifies how a runtime demonstrates that it implements
[SPEC-v3](SPEC-v3.md) and the [capability pages](capabilities/com.docker.sandbox/).

The key words **MUST**, **MUST NOT**, **SHOULD**, and **MAY** are to be
interpreted as in RFC 2119.

Conformance has two halves, and they are independent. A Kit is judged
against the artifact rules; a runtime is judged against the behavior its
capability pages require. A runtime that consumes Kits it did not build is
still responsible only for the second.

## 1. Kit conformance

An artifact conforms when it satisfies [§9](SPEC-v3.md#9-publishing) and
[§10](SPEC-v3.md#10-the-oci-layout). `kit-tck validate <reference>` judges a
published artifact; the reference implementation's BuildKit frontend runs
the same checks before it exports, so a Kit built by it cannot be
published malformed.

Running both matters. Build-time checks see the descriptor, the recipe,
and the filesystem about to be exported, and can point at the authored
line that is wrong. Only a published artifact shows what the exporter and
the registry did with it — and an artifact from a different producer has
no build to check.

## 2. Runtime conformance

A runtime demonstrates conformance by supplying an **adapter**: an
executable implementing the verbs below. The suite drives the adapter and
asserts observable behavior, so a runtime conforms by what it does, not by
how it is written. An adapter **MAY** be a shell script.

```sh
kit-tck runtime --adapter ./my-runtime-adapter
```

### 2.1 Invocation

The suite invokes the adapter as `<adapter> <verb> [arguments…]`. An
adapter **MUST** write diagnostics to stderr so a failure is attributable,
and anything a verb is specified to return to stdout.

Exit status carries meaning beyond success and failure:

| Exit | Means |
|---|---|
| `0` | The verb succeeded |
| `2` | The runtime **refused** the request, deliberately and by policy |
| any other non-zero | The verb failed |

The distinction is load-bearing. Several requirements are satisfied only
by refusing something — a Kit requiring a capability the runtime does not
implement, most importantly — and a suite that accepted any non-zero exit
as a refusal would pass an adapter that fails at everything, including one
that cannot find its fixtures. An adapter **MUST** exit `2` when it
declines a request it understood, and **MUST NOT** exit `2` for an error.

An adapter **MUST NOT** require interactive input.

### 2.2 Verbs

| Verb | Arguments | stdout | Purpose |
|---|---|---|---|
| `capabilities` | — | one capability type per line | What the runtime claims to implement |
| `create` | `<kit-ref>…`, zero or more `--arg name=value`, at most one `--skills-host-mode readonly\|off` | one sandbox id | Compose the Kit set and start it |
| `exec` | `<id> -- <argv>…` | the command's stdout | Run a command inside |
| `stop` | `<id>` | — | Stop without discarding state |
| `start` | `<id>` | — | Start a stopped sandbox |
| `recreate` | `<id>` | — | Replace the sandbox's container with a fresh writable layer, preserving only declared volume state |
| `rm` | `<id>` | — | Discard the sandbox |
| `wait-idle` | `<id>` | — | Disconnect the final client session and wait beyond the normal auto-stop grace period |
| `status` | `<id>` | `running` or `stopped` | Observe sandbox state without starting it or attaching a session |

`capabilities` is what makes a partial implementation testable: the suite
skips the types a runtime does not claim, and asserts that a Kit
**requiring** an unclaimed type is refused rather than silently
under-provisioned.

`exec` **MUST** proxy the command's exit status as its own, and **MUST
NOT** allocate a TTY. The argv after `--` is passed through unmodified.
It **MUST** run the command as the sandbox's agent identity: several
requirements are about who the sandbox does things as, and an `exec` of
the runtime's choosing would answer for a user the agent never is.

`stop` followed by `start` **MUST** preserve the sandbox's filesystem.
This pair is what separates the lifecycle phases: `install` hooks run once
at create, `startup` hooks on every boot.

`recreate` **MUST** replace the container — discarding the writable layer —
while preserving declared volume state. Because stop/start preserves
everything, it cannot distinguish a volume from an ordinary directory;
recreate is the observation that can, and the suite verifies the layer was
really discarded before crediting anything to the volume.

`wait-idle` and `status` are required only for adapters claiming
`com.docker.sandbox/long-running@1`. `wait-idle` **MUST** exercise a real
client session's connection and disconnection, leave no client sessions
attached, and return only after the runtime's ordinary session auto-stop
grace period has elapsed, with a margin for scheduling. The adapter knows
that period; a fixed suite delay cannot bound every runtime's policy.
A runtime with no session-based auto-stop needs no grace-period wait.

While waiting, the adapter **MUST NOT** use exec, attach, keepalives, or
other operations that reset the idle timer or restart the sandbox. It
**MUST NOT** disable auto-stop or force detached mode to make the check
pass: the runtime under test decides from the descriptor whether to keep
the workload running. Session types with different disconnect paths
**MUST** each be exercised before `wait-idle` returns, with a full idle
interval after each final disconnection.

`status` **MUST** read host-side state without starting, resuming, or
attaching to the sandbox. It reports `stopped` only when the sandbox has
finished stopping; a missing sandbox or a failed observation is an error,
not a stopped state. The suite reads status before probing the background
process, so an exec that implicitly starts a stopped sandbox cannot hide
auto-stop, and reads it again after explicit stop.

### 2.3 Known values the suite arranges

Several requirements are about what must **not** appear, and absence
cannot be judged against an unknown value. The suite therefore places
three known values in the adapter's environment before it judges
anything:

| Variable | Meaning for the adapter |
|---|---|
| `KIT_TCK_HOST_SENTINEL` | A host-side value. A runtime that leaks its own environment into a lifecycle hook leaks this with it, which is how "a hook sees only its declared env" is judged. The adapter does nothing with it beyond letting it be inherited. The suite also decorates baseline variables in the adapter's environment — `TERM`, `HOSTNAME`, `OLDPWD`, `SHLVL`, and `_` carry the sentinel, and `PATH` gains a sentinel component — so a runtime copying a host baseline value into a hook is caught by the same scan; baseline values must derive from the image and sandbox. `HOME` and `PWD` are both: they point through a sentinel-named symlink to the real home, functional for credential helpers and relative paths while still betraying host provenance when copied. |
| `KIT_TCK_BOUND_SECRET` | The secret the adapter **MUST** bind for the fixture credential service `kit-tck`. The container **MUST NOT** see this value; a proxy-managed credential with a declared name presents a sentinel in that variable, and an inject-only credential (no name) presents nothing at all. |
| `KIT_TCK_SKILL_NAME` | A skill the adapter **MUST** place in the host's shared skills store before the first `create`, when it claims `com.docker.sandbox/agent-skills@1`. The capability permits no mount when the store is empty or skills are off, so without a known entry the suite cannot tell a mounted store from an empty directory. Before the create rather than before the claim: `capabilities` observes nothing and writes nothing, and a query that seeded a user's store would leave an entry behind on every run that asked what a runtime implements. |

An adapter claiming `com.docker.sandbox/agent-skills@1` **MUST** default
skills to their **most permissive** setting. Access is the narrower of the
host's setting and the Kit's, so a restrictive default makes the Kit's
half unobservable: a read-only mount would prove nothing about whether the
runtime honored a Kit asking for read-only, or merely never offered write
to anyone.

The host's half is judged separately: when `create` carries
`--skills-host-mode`, the adapter **MUST** arrange that host setting for
that sandbox — `readonly` withholds write however much a Kit asked for,
and `off` withholds the store, which for a required entry means the
runtime refuses the create, while an optional entry is skipped and the
sandbox starts without the mount. Without this input the suite could never
observe host-side narrowing at all. The store the suite uses is its own; a
runtime **SHOULD NOT** point these fixtures at a store a user depends on.

The workload fixture declares its working directory as
`/home/agent/workspace`, and the suite reads the agent-context profile
beside it, at `/home/agent/AGENTS.md`. A runtime is free to place
workspaces wherever it likes for its own workloads; for THIS workload, the
declared workdir is the workspace, so the profile's place beside it is a
path the suite can name.

An adapter that cannot bind credentials **SHOULD NOT** claim
`com.docker.sandbox/credential@1`, in which case its checks are skipped.

### 2.4 What the suite guarantees

The suite **MUST** `rm` every sandbox it creates, including after a
failure. It **MUST NOT** assume any state carries between test cases, and
addresses sandboxes only by the ids `create` returned.

### 2.5 Kit references

The suite builds its fixture Kits through the runtime under test, because
a runtime that consumes Kits necessarily has a way to obtain them. A
`create` argument is therefore whatever reference form the runtime accepts
— a local directory, an image reference — and an adapter **MAY** pass it
through unchanged.

## 3. Reporting conformance

A runtime claiming conformance **SHOULD** state which capability types it
implements and publish the suite's output. A runtime implementing a subset
is conforming for the types it claims, provided it refuses what it cannot
provide: silently ignoring a required capability is the one failure the
model cannot tolerate, because the Kit's author declared it precisely
because the Kit does not work without it.
