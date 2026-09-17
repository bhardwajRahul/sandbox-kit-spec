# `com.docker.sandbox/sbx@1`

A declaration that the workload targets the sandbox agent platform: the
host launches the agent rather than letting the image entrypoint be PID 1,
and it honors the identity the image config states instead of assuming one.

- **Shape**: singleton, **config-less** — any `config` value is a
  validation error.
- **Permission surface**: **no** — it asks the host to run the workload a
  particular way and to read what the image already declares. Nothing
  crosses the boundary the gate guards.

Declaring it is a claim in both directions, which is what makes it
checkable from both sides. The kit asserts its filesystem can be operated
this way; the host owes the duties below. A runtime that cannot perform
them refuses the type by name rather than approximating it.

## Config

```yaml
- type: com.docker.sandbox/sbx@1
```

No `config`. The identity the host must honor is the image config's
`user`, which images already express; restating it here would create two
answers to one question, and one of them would go stale.

## What the kit provides

A workload declaring this type:

- **MUST** ship a POSIX shell at `/bin/sh` that the declared user can <!-- tck: sbx@1/posix-shell-present -->
  execute. The host runs hooks, install steps, and its own idle process
  through it. Occupying the path is not enough: a FIFO, socket, or device
  node, or bits that leave the declared user out, resolve here and then
  fail at the first hook. A link counts when what it resolves to is an
  ordinary file that user can execute.
- **MUST** ship a `bash` at `/bin/bash` the declared user can execute. <!-- tck: sbx@1/bash-present -->
  The agent is launched under bash specifically, because bash is what
  sources `BASH_ENV`; a POSIX shell would start the agent without its
  persistent environment.
- **MUST** declare a non-empty `user` in its image config. An image that <!-- tck: sbx@1/image-declares-user -->
  declares none leaves the host nothing to honor, and the host would be
  back to assuming.
- **MUST** resolve that user in `/etc/passwd` to a uid, a gid, and a home <!-- tck: sbx@1/user-resolves-in-passwd -->
  path. The host reads all four out of the image: the uid before the
  container exists, since it lands in the container's environment; the
  name to run commands as a login user; the gid to own what it writes;
  and the home as the working directory it writes from. A user the image
  names but does not resolve leaves the host guessing the rest.
  Resolution follows what a runtime does with the spelling: a numeric
  user is a uid rather than a login name, a `user:group` suffix is the
  gid that applies instead of the passwd primary and resolves against
  `/etc/group` when it is named, and a row with no login name, a uid or
  gid outside the 32-bit range a host can hold, or a home that is not
  absolute has not resolved anything.
- **SHOULD** name the file by absolute path in `BASH_ENV` and ship it. <!-- tck: sbx@1/bash-env-names-a-shipped-file -->
  Without it the agent starts with whatever the image config carries and
  nothing the sandbox adds later, and a relative value resolves against
  whatever directory the agent happens to run from rather than against
  the image.

## Runtime behavior

A conforming runtime:

- **MUST NOT** let the image's entrypoint become PID 1. The entrypoint <!-- tck: sbx@1/entrypoint-not-pid-one -->
  names the agent's launch command, which the host reads and runs later;
  left in place it would prepend itself to whatever the host runs as PID 1.
- **MUST** run the agent, hooks, and file writes as the user the image <!-- tck: sbx@1/honors-image-user -->
  config declares — resolving its uid, gid, name, and home from the
  image — rather than as a fixed identity of the runtime's choosing.
- **MUST** launch the agent under `bash` so the file named by `BASH_ENV` <!-- tck: sbx@1/agent-launched-under-bash -->
  is sourced. The agent is not started from a login or interactive shell,
  so profile and rc files never run; this is the only thing that loads it.
- **MUST** place the workspace at the image config's working directory <!-- tck: sbx@1/workspace-at-workdir -->
  when the image declares an absolute one, so the kit's paths and the
  host's agree. Observable wherever the host writes into the workspace,
  such as an `agent-context@1` profile.
- **MAY** refuse the type. Refusal fails resolution for a required entry;
  an optional entry is skipped and recorded, and the kit runs however the
  host runs an ordinary image.

## Composition

Singleton, and a workload concern: the kit whose layers are the root
filesystem is the one whose image config carries the identity. A mixin
declaring it says nothing a host can act on, because a mixin's image
config does not become the composed image's.

## Gate

Not permission surface, so declaring it is not a widening. What it changes
is how the host runs the workload, not what the workload may reach.
