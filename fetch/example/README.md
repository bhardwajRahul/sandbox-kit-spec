# Resolve Kits and assemble an image through the Go API

[main.go](main.go) is a complete runnable consumer. It reads OCI
references and per-Kit create arguments from stdin, authenticates using
Docker's credential store, selects capabilities through an explicit
callback, resolves validated declarations, and assembles image metadata.

From the repository root, substitute references you can read. The tool
Kit below is assumed to declare a create-phase `team` argument; use the
argument names declared by your own Kits.

```sh
go run ./fetch/example <<'JSON'
[
  {"Reference": "docker.io/your-org/workload:1.0.0"},
  {
    "Reference": "docker.io/your-org/tool:1.0.0",
    "Args": {"team": "alpha"}
  }
]
JSON
```

The preview accepts library-known types by default. Use
`-supported-types=type1,type2` to supply a runtime's actual supported
types. Add `-allow-volumes=false` to simulate a host rejecting persistent
storage: an optional cache group then contributes neither its volume nor
its lifecycle configuration file. A required rejected entry or group
fails resolution. These flags control the preview; it does not inspect
the host or apply capabilities.

The core API flow is:

```go
client, err := fetch.New(fetch.WithDockerCredentials())
if err != nil {
    return err
}
// Replace the preview's list and policy with the runtime's own decisions.
supported := spec.Supported(spec.KnownCapabilities()...)
allowVolumes := false
selectCapability := func(capability spec.Capability) bool {
    if !supported(capability) {
        return false
    }
    if capability.Type == spec.CapabilityVolume {
        return allowVolumes
    }
    return true
}
resolved, err := client.Resolve(ctx, requests,
    fetch.WithCapabilitySelector(selectCapability))
if err != nil {
    return err
}
image, err := assemble.Assemble(ctx, resolved.Kits, client.LoadImage)
if err != nil {
    return err
}
manifest, err := image.Manifest()
if err != nil {
    return err
}
```

`Resolve` reads descriptor annotations, strictly decodes and validates
them, resolves create arguments, expands and validates each descriptor,
resolves the dependency set, calls the selector on expanded entries,
selects whole groups, flattens and composes in dependency order, and
validates the final descriptor. Supply all required Kits: resolution does not
discover missing dependencies. Exactly one Kit is a workload.
`ResolvePartial` allows a mixin-only set and still performs every check.
Image assembly requires a workload.

The client defaults to Linux on the caller's architecture. Pass
`fetch.WithPlatform` to select another platform. Public registries can
use `fetch.New()` without credentials.

The program prints three values:

- `Resolved`: the selected, merged `Descriptor`, dependency-ordered
  selected `Kits`, original declarations and records in `Selections`,
  `ContainerEnv`, and validation `Warnings`. Each selected Kit retains its
  reference, digest-pinned image, expanded descriptor, and resolved
  arguments including defaults.
- `Image`: typed OCI `Config` and ordered `Layers`. The workload's
  layers come first, followed by mixins in dependency order.
- `Manifest`: an OCI manifest computed from the current image config
  and layer references.

Use `Resolved.Descriptor` for reconciled capabilities. Handlers that
need attribution can inspect each input in `Resolved.Kits`. Preserve
those inputs when serializing the result: some per-Kit declarations
cannot fit into a reconciled singleton entry.

`Resolved.ContainerEnv` contains resolved arguments explicitly exported
with `env:`. Apply these as overrides when creating the container,
after image environment defaults. Assembly leaves them separate from
`Image.Config`. Conflicting Kit exports are errors; identical values
coalesce. Arguments without `env:` only expand the descriptor.

`LoadImage` reads the platform manifests and config blobs using the same
credentials and platform as resolution. Assembly uses the pinned image
references, so moving a tag after resolution does not change its inputs.
A local runtime can supply its own `assemble.ImageLoader` instead.

`image.Manifest()` computes the config digest and size internally.
After changing `image.Config`, call it again to obtain a fresh snapshot.
For storage, `image.WriteMetadata(ctx, destination)` accepts an ORAS
`content.Pusher` and writes the config and manifest from one
serialization snapshot, returning the manifest's OCI descriptor.
Consumers do not handle a separate serialized config field.

Neither resolution nor image assembly downloads filesystem layers.
The runtime supplies layer transfer, filesystem collision checks,
image naming/import, and container creation. A metadata destination
must have access to the referenced layers; `WriteMetadata` does not
copy them.

Validation is always enabled. Errors retain their structured causes,
and descriptor validation errors include locations and source excerpts,
so print the returned error directly.

Publishing a flattened Kit remains `spec.Merge`'s job. Its
`MergeOptions.ContextPath` belongs to the publisher that stages the
combined body; runtime consumers do not choose a publishing path.

For runtime selection, pass
`fetch.WithCapabilitySelector(spec.Supported(claimedTypes...))` to
`Resolve` or `ResolvePartial`. A custom callback can also inspect each
expanded config and apply host policy. The default accepts types known
to this library; it does not inspect host availability.

For already loaded descriptors, use
`spec.SelectCapabilities(descriptor, selector)`. Selection receives the
whole descriptor so it can validate kind-specific rules before invoking
policy, including workload-only capabilities and context profiles.

Persist `Resolved.Descriptor` and `Resolved.Selections` with the sandbox
and reuse that selection on restart. `Resolved.Kits` contains
only selected, unmerged contributions; `Selections` retains the original
declarations for diagnostics. Apply hooks from `Resolved.Descriptor`,
and use `spec.AgentContextsOf(kit.Descriptor.Capabilities)` to enumerate
all selected per-Kit guidance bodies. Do not apply the original
unselected declarations on startup or during image assembly.
