package main

import (
	"bytes"
	"context"
	"fmt"

	gwclient "github.com/moby/buildkit/frontend/gateway/client"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// derivedProvides reads what every platform's filesystem says it carries
// and renders the provides entries they agree on (§9.6).
//
// Read from the built filesystems rather than declared, because a base
// image's contents are not something an author knows: a kit that names
// `bash` in provides is stating a fact about content it inherited, and
// the only honest version for it is the one dpkg recorded. Nothing is
// derived for a mixin — its delta is not a root filesystem, and §5.3
// admits one workload per composition, so a workload-only rule gives
// every derived name exactly one owner.
func derivedProvides(ctx context.Context, refs []gwclient.Reference) ([]string, error) {
	var out []string
	for _, db := range spec.PackageDatabases() {
		listings := make([][]spec.Package, 0, len(refs))
		for _, ref := range refs {
			// A declaration-only kit has no filesystem to read, which is
			// not the same as one whose filesystem holds no packages: the
			// first cannot be judged, and treating it as an empty listing
			// would make every platform disagree with it.
			if ref == nil {
				return nil, nil
			}
			body, present, err := readImageFile(ctx, ref, db.Path)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", db.Path, err)
			}
			if !present {
				// An image with no such database installs through
				// something else — a DHI rootfs assembled from debs
				// carries no dpkg at all — and has nothing to say here.
				listings = nil
				break
			}
			packages, err := db.Read(bytes.NewReader(body))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", db.Path, err)
			}
			listings = append(listings, packages)
		}
		out = append(out, spec.DerivedProvides(db.Namespace, listings)...)
	}
	return out, nil
}
