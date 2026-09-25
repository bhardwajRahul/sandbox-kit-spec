# `com.docker.sandbox/long-running@1`

The workload runs independently of client sessions. A background startup
hook or a published port alone does not request this behavior: on a
runtime with session-based auto-stop, the sandbox can otherwise stop when
its last client disconnects.

- **Shape**: singleton, **config-less**, workload-only.
- **Permission surface**: **no** — it changes when the host stops the
  workload, without granting access across the sandbox boundary.

## Config

```yaml
capabilities:
  - type: com.docker.sandbox/long-running@1
```

The entry is required by default. Use `optional: true` only when the
workload can tolerate ordinary session-based auto-stop.

- The entry **MUST NOT** carry `config`, including an empty or null <!-- tck: long-running@1/no-config -->
  value.
- A validator **MUST** reject the entry on a mixin. Only the resolved <!-- tck: long-running@1/workload-only -->
  workload can declare that its sandbox runs independently of sessions.
  An authored set can declare it pending kind resolution; a set resolving
  to a mixin is rejected.

## Runtime behavior

A conforming runtime:

- **MUST NOT** automatically stop a sandbox granted this capability <!-- tck: long-running@1/survives-session-disconnect -->
  solely because no interactive, agent, exec, or other client session
  remains attached. Background workload processes keep running across
  that disconnection, including beyond the normal auto-stop grace period.
- **MUST** honor explicit stop operations for that sandbox. <!-- tck: long-running@1/explicit-stop-honored -->
  The capability also leaves runtime shutdown and recovery, failure
  handling, and explicit removal subject to the runtime's normal policy.
- **MUST** refuse a required entry it cannot provide during capability <!-- tck: long-running@1/required-unsatisfiable-refused -->
  preflight, by type name, before starting the workload. An optional
  entry it cannot provide is skipped and recorded; ordinary session
  auto-stop may then apply.

A runtime without session-based auto-stop already supplies the behavior,
but still advertises the type before accepting it as required. Detached
mode, a durable lease, or a service manager can implement the contract;
the capability prescribes none of them. It does not request automatic
restart after a failure or promise uninterrupted availability.

## Composition

The resolved workload owns the declaration. Mixins cannot impose a
sandbox lifetime on the workload they extend. Identical declarations
from a workload and its enclosing set collapse; a required declaration
wins over an optional one.

## Gate

Not permission surface. The host can refuse to supply the behavior, just
as it can refuse other engine-executed capabilities.
