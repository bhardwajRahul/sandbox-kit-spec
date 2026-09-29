package assemble

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/errdef"
)

// Image is the composed image configuration and its ordered layer references.
// Container environment overrides are applied separately at container creation.
type Image struct {
	Config ocispec.Image
	Layers []ocispec.Descriptor
}

// Manifest computes the config reference from the current Config. Call it
// again after changing Config or Layers; the returned manifest is a snapshot.
func (image *Image) Manifest() (ocispec.Manifest, error) {
	manifest, _, err := image.finalize()
	return manifest, err
}

// WriteMetadata writes the current config and manifest into a content store
// and returns the manifest's descriptor. It does not transfer layers or tag
// the image; the caller must make the referenced layers available in the
// destination. Config and manifest use the same serialization snapshot.
func (image *Image) WriteMetadata(ctx context.Context, dst content.Pusher) (ocispec.Descriptor, error) {
	if dst == nil {
		return ocispec.Descriptor{}, fmt.Errorf("write image metadata: no destination")
	}
	manifest, config, err := image.finalize()
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("marshal image manifest: %w", err)
	}
	desc := ocispec.Descriptor{MediaType: manifest.MediaType, Digest: digest.FromBytes(raw), Size: int64(len(raw))}
	if err := dst.Push(ctx, manifest.Config, bytes.NewReader(config)); err != nil && !errors.Is(err, errdef.ErrAlreadyExists) {
		return ocispec.Descriptor{}, fmt.Errorf("write image config: %w", err)
	}
	if err := dst.Push(ctx, desc, bytes.NewReader(raw)); err != nil && !errors.Is(err, errdef.ErrAlreadyExists) {
		return ocispec.Descriptor{}, fmt.Errorf("write image manifest: %w", err)
	}
	return desc, nil
}

func (image *Image) finalize() (ocispec.Manifest, []byte, error) {
	if image == nil {
		return ocispec.Manifest{}, nil, fmt.Errorf("finalize image: no image")
	}
	if len(image.Layers) != len(image.Config.RootFS.DiffIDs) {
		return ocispec.Manifest{}, nil, fmt.Errorf("finalize image: %d layers but %d config diff_ids", len(image.Layers), len(image.Config.RootFS.DiffIDs))
	}
	raw, err := json.Marshal(image.Config)
	if err != nil {
		return ocispec.Manifest{}, nil, fmt.Errorf("marshal image config: %w", err)
	}
	manifest := ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageConfig,
			Digest:    digest.FromBytes(raw), Size: int64(len(raw)),
		},
		Layers: slices.Clone(image.Layers),
	}
	return manifest, raw, nil
}
