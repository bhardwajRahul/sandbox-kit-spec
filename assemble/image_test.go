package assemble

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
)

func TestImageMetadataTracksConfigChanges(t *testing.T) {
	image, err := Merge(input("base", "layer"), nil)
	require.NoError(t, err)
	before, err := image.Manifest()
	require.NoError(t, err)
	image.Config.Config.Env = []string{"TEAM=changed"}
	after, err := image.Manifest()
	require.NoError(t, err)
	require.NotEqual(t, before.Config.Digest, after.Config.Digest)
	store := memory.New()
	desc, err := image.WriteMetadata(t.Context(), store)
	require.NoError(t, err)
	raw, err := content.FetchAll(t.Context(), store, desc)
	require.NoError(t, err)
	require.Equal(t, digest.FromBytes(raw), desc.Digest)
	var manifest ocispec.Manifest
	require.NoError(t, json.Unmarshal(raw, &manifest))
	require.Equal(t, after, manifest)
	config, err := content.FetchAll(t.Context(), store, manifest.Config)
	require.NoError(t, err)
	expected, err := json.Marshal(image.Config)
	require.NoError(t, err)
	require.Equal(t, string(expected), string(config))
	require.Equal(t, int64(len(config)), manifest.Config.Size)
	again, err := image.WriteMetadata(t.Context(), store)
	require.NoError(t, err)
	require.Equal(t, desc, again, "unchanged metadata is deterministic and already-present blobs are accepted")
}

type pushFunc func(context.Context, ocispec.Descriptor, io.Reader) error

func (fn pushFunc) Push(ctx context.Context, desc ocispec.Descriptor, r io.Reader) error {
	return fn(ctx, desc, r)
}

func TestImageWriteMetadataPropagatesStorageFailures(t *testing.T) {
	image, err := Merge(input("base", "layer"), nil)
	require.NoError(t, err)
	for _, failAt := range []int{1, 2} {
		calls := 0
		failure := errors.New("storage failure")
		_, err := image.WriteMetadata(t.Context(), pushFunc(func(context.Context, ocispec.Descriptor, io.Reader) error {
			calls++
			if calls == failAt {
				return failure
			}
			return nil
		}))
		require.ErrorIs(t, err, failure)
		require.Equal(t, failAt, calls)
	}
}

func TestImageRejectsInconsistentLayerStack(t *testing.T) {
	image, err := Merge(input("base", "layer"), nil)
	require.NoError(t, err)
	image.Layers = nil
	_, err = image.Manifest()
	require.ErrorContains(t, err, "0 layers but 1 config diff_ids")
	_, err = image.WriteMetadata(t.Context(), memory.New())
	require.ErrorContains(t, err, "0 layers but 1 config diff_ids")
}
