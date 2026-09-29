# `com.docker.sandbox/agent-skills-directory@1`

A directory an agent scans for bundled skills. Declare this on the Kit
that supplies the agent, whether it is a workload or a mixin.

- **Shape**: instance — keyed by path.
- **Permission surface**: no — an in-sandbox destination for Kit content.

## Config

```yaml
- type: com.docker.sandbox/agent-skills-directory@1
  config:
    path: /home/agent/.claude/skills
```

| Field | Type | Rules |
|---|---|---|
| `path` | string | REQUIRED. Absolute, canonical in-container discovery directory; not `/`, no trailing slash, repeated separator, or `.` or `..` segments. |

## Runtime behavior

A runtime **MUST** use every selected `path` as a destination for selected <!-- tck: agent-skills-directory@1/destination -->
[agent-skill@1](agent-skill@1.md) requests. A destination with no selected
bundled skills requires no filesystem change. An unavailable destination
follows ordinary required/optional selection rules; a bundled skill with
no remaining destination is unsatisfiable.

This declaration does not ask for host store access. To request that too,
declare [agent-skills@1](agent-skills@1.md) separately at the same path.
The host's sharing setting governs that request, not bundled skills.
The runtime decides how to expose both kinds of content together while
honoring their availability, access, and conflict rules.

## Composition

Paths union across Kits, with identical paths satisfied once. Required
wins over optional. Within a single declaration block duplicate paths
are errors. Every selected directory receives every selected bundled
skill; there is no implicit agent identity or agent-specific filtering.
