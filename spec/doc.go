// Package spec defines the kit v3 descriptor: the declaration half of a kit,
// published as a manifest annotation on an ordinary OCI image whose layers
// are the kit's content.
//
// The descriptor deliberately carries no name, no version, no image
// reference, and no runtime config (entrypoint, env, workdir, user, default
// command): identity is the reference a kit is consumed by, and runtime
// config lives in the image config, set by the companion Dockerfile.
//
// Decode and Validate read a descriptor. Merge folds an ordered set of
// them into the one descriptor a composition publishes. Ordering that set
// is resolve; reading the descriptors out of a registry is fetch; folding
// the images themselves is assemble.
package spec
