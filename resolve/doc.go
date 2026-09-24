// Package resolve validates and orders a closed set of kits.
//
// Capability names resolve to nothing and validate against something: the
// user names the kits (the closed set), and this package checks that the
// set is coherent — every requirement satisfied, no conflicts, exactly one
// workload kit — and derives the one deterministic order the kits compose in.
// No registry IO happens here; callers hand in already-fetched descriptors.
//
// Resolve requires one workload. ResolvePartial is the same judgment for
// a set of mixins that will land on a workload later. spec.Merge folds
// the ordered descriptors; assemble.Merge folds the images. fetch turns
// registry references into the descriptors this package takes.
package resolve
