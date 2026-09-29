# Assemble Kits through the Go API

[main.go](main.go) is a complete runnable consumer. It reads OCI
references and per-Kit create arguments from stdin, authenticates using
Docker's credential store, selects capabilities, composes the image and
declarations, and checks file collisions across the Kits' layers.

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

## One-call API

```go
result, err := fetch.Assemble(ctx, requests, fetch.Options{
    CapabilitySelector: spec.Supported(claimedTypes...),
    Overrides: fetch.Overrides{
        Env: map[string]string{"WORKSPACE_DIR": "/workspace"},
    },
    OnProgress: func(p fetch.Progress) {
        // Render p.Stage, p.State, p.Reference, and p.Layer in the UI.
    },
})
if err != nil {
    return err
}
manifest, err := result.Image.Manifest()
if err != nil {
    return err
}
```

`fetch.Options{}` is sufficient for registry loading and acceptance of
all capability types the library knows. That default is not a claim that
a runtime implements every type: supply its actual supported types or a
policy callback. Selection sees expanded configurations and must not
apply effects. The library validates even skipped declarations, selects
groups atomically, and validates the selected composition. Conflicts
fail; optional groups are not dropped to repair them.

Supply the complete dependency set, containing exactly one workload.
`Assemble` does not discover missing dependencies or publish an image.
It loads verified metadata, resolves arguments and capability decisions,
composes image defaults, reads layer inventories, and rejects files
contributed by multiple Kits. Collision checks resolve each Kit's layers
with the shared overlay filesystem model before comparing them in image
order: cleaned paths, symlink aliases, whiteouts, opaque directories,
and file/directory replacements are included. Directories may overlap.
Files deleted within a Kit no longer claim paths, but its surviving
deletion effects cannot erase another Kit's content. A layer shared by
multiple inputs with the same expected diff ID is read once, but each
Kit retains its file ownership for the collision check. Skipping
capabilities removes neither layers nor argument environment exports.

Each layer inventory is limited to 250,000 archive entries and 32 MiB of
combined path and link-name bytes, counting repeated entries. Assembly
fails when either limit is exceeded, before caching the inventory, and
closes the layer stream. These limits bound retained metadata even when
a small compressed layer repeatedly overwrites the same path.

The program prints the result plus its computed manifest:

- `Resolved`: the selected `Descriptor`, dependency-ordered per-Kit
  `Kits`, original declarations and decisions in `Selections`, argument
  `ContainerEnv` exports, and validation `Warnings`.
- `Image`: typed OCI `Config` and ordered `Layers`. The workload's
  layers come first, followed by mixins in dependency order.
- `Environment`: the complete container environment, combining image
  defaults, argument exports, and `Overrides.Env`, in that precedence.
- `WorkingDir`: the workload image's working directory, or the explicit
  absolute `Overrides.WorkingDir` when supplied.
- `Manifest`: a snapshot computed from the image defaults and layers.

Environment and working-directory overrides do not modify the reusable
image. Apply the returned container settings at creation. Empty
variable values remain empty; missing override keys retain their values.
Values are literal: this API does not evaluate shell expressions or
`${{ kit.env.* }}` placeholders. Those placeholders are not supported.

Persist `Resolved.Descriptor` and `Resolved.Selections` with the sandbox
and reuse the decision on restart. A recreation selects afresh. Apply
hooks from `Resolved.Descriptor`; use
`spec.AgentContextsOf(kit.Descriptor.Capabilities)` for each selected
Kit's guidance bodies. Do not execute original, unselected declarations.
Use `resolve.Resolve(result.Resolved.Kits)` with the existing lock/gate
APIs to judge permissions before applying the result.

## Loading and progress

`Options.Loader` accepts a `fetch.KitLoader`. Its `LoadedKit` contains
verified digest identity, manifest, config, and a lazy `OpenLayer`
function. `Descriptor` optionally carries the original annotation from
an index; otherwise the platform manifest's annotation is used. A loader
must resolve metadata consistently to the returned digest and select a
platform. Assembly validates the metadata and declarations; it streams,
verifies, and closes layer blobs without buffering their bodies.

The default loader uses Docker credentials and Linux on the caller's
architecture. Customize registry behavior with the existing client:

```go
client, err := fetch.New(
    fetch.WithDockerCredentials(),
    fetch.WithPlatform(platform),
)
if err != nil {
    return err
}
result, err := fetch.Assemble(ctx, requests, fetch.Options{
    Loader: client.LoadKit,
})
```

For anonymous registries use `fetch.New()`. For a local content store,
supply a loader that opens blobs from that store. `OpenLayer` returns
fresh streams in the manifest's original compression and honors the
provided context. Assembly accepts tar, gzip, and zstd. It verifies both
the manifest's stored-blob digest and the config's uncompressed diff ID,
including archive padding and compression trailers. Every opened stream
is closed on success or failure; read and close errors fail the operation.
An archive entry the shared extractor model refuses fails assembly.

`OnProgress` receives serialized stage transitions on the calling
goroutine: `started`, `completed`, or `failed`. The stages are `load`,
`resolve` (including argument expansion, selection, and descriptor
validation), `compose`, `inventory`, and `collisions`. Kit references and
layer digests identify individual work. Cached inventories emit no new
event. Callbacks should return promptly; use the context to cancel. The
returned error remains authoritative, and events never contain
configuration values or file contents.

## Storage and lower-level APIs

`result.Image.Manifest()` computes the config digest and size internally.
After changing the image config, call it again for a fresh snapshot.
`Image.WriteMetadata(ctx, destination)` accepts an ORAS `content.Pusher`
and writes config and manifest from one serialization snapshot. The
caller supplies layer transfer, naming/import, and container creation;
the destination must have access to all referenced layers.

For callers orchestrating those steps themselves, `Client.Resolve` and
`ResolvePartial` resolve only declarations. `Client.LoadImage` and
`assemble.Assemble` load and compose image metadata without downloading
layers. These existing APIs remain available. The one-call API also
checks layer inventories, so it reads more data than metadata-only
assembly.

Errors retain structured causes. Descriptor errors include original
locations and source excerpts; print the returned error directly.
Publishing a flattened Kit remains `spec.Merge`'s job, with staged
context bodies owned by the publisher. Runtime composition uses
`spec.Compose` and preserves each selected context source separately.
