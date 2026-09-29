package fetch

import (
	"context"
	"fmt"
	"io"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// KitLoader supplies verified metadata for the requested reference. It owns
// authentication, platform selection, and its backing store. The returned
// digest pins the requested manifest or index, not a subsequently moved tag.
// Assemble always validates declarations. ValidateLayers verifies both blob
// digests and diff IDs when selected as Options.LayerValidator.
type KitLoader func(context.Context, string) (*LoadedKit, error)

// LayerLoader opens a fresh stream of a manifest layer in its original
// compression. Reads must honor ctx; the caller closes the returned stream.
type LayerLoader func(ctx context.Context, layer ocispec.Descriptor) (io.ReadCloser, error)

// LoadedKit keeps descriptor identity and image metadata together. Its data
// must remain stable for the duration of Assemble; Assemble does not mutate it.
type LoadedKit struct {
	Digest   digest.Digest
	Manifest ocispec.Manifest
	Config   ocispec.Image
	// Descriptor is the published annotation, including an index annotation when
	// present. If empty, Assemble reads the selected manifest's annotation.
	Descriptor []byte
	// LayerLoader opens a fresh stream of the blob as described by the manifest,
	// with its original compression. ValidateLayers closes each returned stream.
	// Nil is allowed for metadata-only assembly; ValidateLayers requires it.
	LayerLoader LayerLoader
}

// LoadKit implements KitLoader with this client's credentials and platform.
// Image metadata is loaded by the resolved digest to prevent tag movement
// between descriptor and image reads. Filesystem layers are opened lazily.
func (c *Client) LoadKit(ctx context.Context, ref string) (*LoadedKit, error) {
	kit, err := c.Fetch(ctx, ref)
	if err != nil {
		return nil, err
	}
	pinned, err := pinnedImage(ref, kit.Digest)
	if err != nil {
		return nil, err
	}
	image, err := c.LoadImage(ctx, pinned)
	if err != nil {
		return nil, err
	}
	repoName, _, err := splitRef(pinned)
	if err != nil {
		return nil, err
	}
	repo, err := c.repository(repoName)
	if err != nil {
		return nil, err
	}
	return &LoadedKit{
		Digest: digest.Digest(kit.Digest), Manifest: image.Manifest, Config: image.Config, Descriptor: kit.Raw,
		LayerLoader: func(ctx context.Context, layer ocispec.Descriptor) (io.ReadCloser, error) {
			reader, err := repo.Fetch(ctx, layer)
			if err != nil {
				return nil, fmt.Errorf("open layer %s: %w", layer.Digest, err)
			}
			return reader, nil
		},
	}, nil
}
