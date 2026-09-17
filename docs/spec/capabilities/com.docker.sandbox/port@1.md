# `com.docker.sandbox/port@1`

One in-container port the runtime should publish to the host. Inbound
service exposure — distinct from the outbound
[network policy](network-policy@1.md).

- **Shape**: instance — one entry per (container port, transport);
  duplicates rejected.
- **Permission surface**: yes — `port/transport`.

## Config

```yaml
- type: com.docker.sandbox/port@1
  config:
    name: devserver        # optional informational label
    container: 3000        # REQUIRED, 1..65535
    transport: tcp         # optional: "tcp" (default) | "udp"
```

| Field | Type | Rules |
|---|---|---|
| `name` | string | optional. Label surfaced in port listings. |
| `container` | int | REQUIRED. 1–65535. |
| `transport` | string | optional. `tcp` (default when empty) or `udp`. |

## Runtime behavior

A conforming runtime:

- **MUST** publish the container port to the host when the entry is <!-- tck: port@1/published-when-granted -->
  granted.
- **MUST** allocate the host side itself — a Kit cannot pin a host port <!-- tck: port@1/host-side-allocated-by-host -->
  (two Kits requesting the same one would collide). Host binding SHOULD be <!-- tck: port@1/host-binding-default -->
  loopback with an ephemeral port; users pin host ports through the
  runtime's own UX, not the descriptor.
- **SHOULD** surface `name` wherever published ports are listed. <!-- tck: port@1/name-surfaced -->

## Composition

Entries union across the set, deduplicated on (container, transport). Two
Kits publishing the same port is a single publication, not a conflict.

## Gate

`port/transport` is permission surface. A new port widens.
