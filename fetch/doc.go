// Package fetch reads published Kit descriptors and image metadata from OCI
// registries. Credentials, transport, and platform selection belong to Client.
//
// Resolve fetches a closed set of requests, resolves create arguments, validates
// every input, orders the dependency graph, selects capability groups,
// reconciles the selected descriptors with
// spec.Compose, and validates the final descriptor. ResolvePartial performs the
// same checks while allowing a mixin-only set. There is no validation bypass.
//
// WithCapabilitySelector supplies runtime decisions over expanded entries.
// The default selector accepts this library's known types; runtimes should
// supply their actual claims with spec.Supported or a stricter callback.
// Resolved.Selections retains original declarations and create-time decisions.
// Resolved.Kits retains only selected contributions, pinned image identities,
// and resolved arguments in dependency order. Resolved.ContainerEnv contains
// env: exports for container creation; these override image defaults at runtime.
// Resolved.Warnings contains advisory findings from final validation.
//
// LoadImage implements assemble.ImageLoader using the same credentials and
// platform as descriptor resolution. Pass it to assemble.Assemble together
// with Resolved.Kits to load the pinned images and compose their metadata.
// Resolve reads manifests only; LoadImage also reads config blobs. Neither
// downloads filesystem layers or performs filesystem collision checks.
//
// A complete runnable consumer lives in fetch/example. Publishing a flattened
// Kit remains spec.Merge's job, with staging owned by the BuildKit frontend.
package fetch
