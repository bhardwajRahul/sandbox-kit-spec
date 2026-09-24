// Package assemble is the manifest arithmetic of kit composition: given the
// sandbox kit's image and the mixins' overlay images, in composition order,
// it computes the merged image's config and manifest. No filesystem work
// happens — every layer already exists as a blob wherever the inputs live;
// assembly writes two small JSON blobs that reference them.
//
// This is the image half of composition. The declaration half is
// spec.Merge, which takes the order resolve derives. fetch is what reads
// a kit's descriptor out of a registry; this package never does.
package assemble
