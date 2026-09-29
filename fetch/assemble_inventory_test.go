package fetch

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestInventoryComponentBudgetIncludesLinkTargets(t *testing.T) {
	budget := inventoryBudget{entriesLeft: 10, pathBytesLeft: 100, componentsLeft: 4}
	require.NoError(t, budget.consume(&tar.Header{Name: "a/b", Linkname: "c/d"}))
	require.Zero(t, budget.componentsLeft)
	require.ErrorContains(t, budget.consume(&tar.Header{Linkname: "e"}), "path components")
	require.ErrorContains(t, budget.consume(&tar.Header{Name: "e"}), "path components")
	// Repeated separators do not allocate nodes; dot segments still cost work.
	require.Equal(t, 4, pathComponents("//a/./b/../"))
}

func TestAssembleRejectsDeepPathsBeforeBuildingFilesystem(t *testing.T) {
	input, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
	var blob bytes.Buffer
	gz := gzip.NewWriter(&blob)
	diffID := digest.Canonical.Digester()
	tw := tar.NewWriter(io.MultiWriter(gz, diffID.Hash()))
	// Each distinct root forces a new chain of implied directories. The old
	// byte and entry limits admit this layer; the component budget refuses it.
	for i := range maxInventoryComponents/2046 + 1 {
		name := fmt.Sprintf("%03d/", i) + strings.Repeat("a/", 2044) + "f"
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0644}))
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	layer := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageLayerGzip, Digest: digest.FromBytes(blob.Bytes()), Size: int64(blob.Len())}
	input.Manifest.Layers = []ocispec.Descriptor{layer}
	input.Config.RootFS.DiffIDs = []digest.Digest{diffID.Digest()}
	reader := &assemblyStream{Reader: bytes.NewReader(blob.Bytes())}
	input.LayerLoader = func(context.Context, ocispec.Descriptor) (io.ReadCloser, error) { return reader, nil }
	var events []Progress
	result, err := Assemble(t.Context(), fixtureRequests(1), Options{LayerValidator: ValidateLayers,
		Loader: fixtureLoader(input), OnProgress: func(p Progress) { events = append(events, p) },
	})
	require.Nil(t, result)
	require.ErrorContains(t, err, "assembly inventory exceeds 250000 path components")
	require.True(t, reader.closed)
	require.Equal(t, Progress{Stage: StageInventory, State: ProgressFailed, Reference: fixtureRequests(1)[0].Reference, Layer: layer.Digest}, events[len(events)-1])
}

func repeatedInventoryLayer(t *testing.T, name string, count int) (ocispec.Descriptor, digest.Digest, []byte) {
	t.Helper()
	var blob bytes.Buffer
	gz := gzip.NewWriter(&blob)
	diffID := digest.Canonical.Digester()
	tw := tar.NewWriter(io.MultiWriter(gz, diffID.Hash()))
	for range count {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0644}))
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	layer := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageLayerGzip, Digest: digest.FromBytes(blob.Bytes()), Size: int64(blob.Len())}
	return layer, diffID.Digest(), blob.Bytes()
}

func TestAssembleInventoryBudgetSpansLayersKitsAndCacheReplays(t *testing.T) {
	for _, limit := range []string{"entries", "path and link-name bytes", "path components"} {
		t.Run(limit, func(t *testing.T) {
			name, count := "file", maxInventoryEntries/2+1
			if limit == "path and link-name bytes" {
				name = strings.Repeat(strings.Repeat("p", 255)+"/", 15) + strings.Repeat("p", 255)
				count = maxInventoryPathBytes/len(name)/2 + 1
			}
			if limit == "path components" {
				name = strings.Repeat("d/", 127) + "f"
				count = maxInventoryComponents/128/2 + 1
			}
			first, firstID, firstBlob := repeatedInventoryLayer(t, name, count)
			second, secondID, secondBlob := repeatedInventoryLayer(t, "x"+name, count)
			for _, arrangement := range []string{"layers", "kits", "cached layers", "cached kits"} {
				t.Run(arrangement, func(t *testing.T) {
					layers := []ocispec.Descriptor{first, second}
					diffIDs := []digest.Digest{firstID, secondID}
					wantReads := 2
					if strings.HasPrefix(arrangement, "cached") {
						layers[1], diffIDs[1], wantReads = first, firstID, 1
					}
					blobs := map[digest.Digest][]byte{first.Digest: firstBlob, second.Digest: secondBlob}
					var readers []*assemblyStream
					open := func(_ context.Context, layer ocispec.Descriptor) (io.ReadCloser, error) {
						r := &assemblyStream{Reader: bytes.NewReader(blobs[layer.Digest])}
						readers = append(readers, r)
						return r, nil
					}
					base, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
					base.Manifest.Layers, base.Config.RootFS.DiffIDs, base.LayerLoader = layers, diffIDs, open
					inputs := []*LoadedKit{base}
					if strings.HasSuffix(arrangement, "kits") {
						mixin, _ := assemblyFixture(t, spec.KindMixin, "tool", "tool")
						base.Manifest.Layers, base.Config.RootFS.DiffIDs = layers[:1], diffIDs[:1]
						mixin.Manifest.Layers, mixin.Config.RootFS.DiffIDs, mixin.LayerLoader = layers[1:], diffIDs[1:], open
						inputs = append(inputs, mixin)
					}
					var events []Progress
					requests := fixtureRequests(len(inputs))
					result, err := Assemble(t.Context(), requests, Options{LayerValidator: ValidateLayers,
						Loader: fixtureLoader(inputs...), OnProgress: func(p Progress) { events = append(events, p) },
					})
					require.Nil(t, result)
					require.ErrorContains(t, err, "assembly inventory exceeds")
					require.ErrorContains(t, err, limit)
					require.Len(t, readers, wantReads)
					for _, r := range readers {
						require.True(t, r.closed)
					}
					require.Equal(t, Progress{Stage: StageInventory, State: ProgressFailed, Reference: requests[len(requests)-1].Reference, Layer: layers[1].Digest}, events[len(events)-1])
				})
			}
		})
	}
}

func TestAssembleInventoryBudgetCountsEmptyLayers(t *testing.T) {
	for _, count := range []int{maxInventoryLayers, maxInventoryLayers + 1} {
		input := layerFixture(t, spec.KindWorkload, "base", []tar.Header{})
		input.Manifest.Layers = slices.Repeat(input.Manifest.Layers, count)
		input.Config.RootFS.DiffIDs = slices.Repeat(input.Config.RootFS.DiffIDs, count)
		open, reads := input.LayerLoader, 0
		input.LayerLoader = func(ctx context.Context, layer ocispec.Descriptor) (io.ReadCloser, error) {
			reads++
			return open(ctx, layer)
		}
		result, err := Assemble(t.Context(), fixtureRequests(1), Options{LayerValidator: ValidateLayers, Loader: fixtureLoader(input)})
		if count == maxInventoryLayers {
			require.NoError(t, err)
			require.NotNil(t, result)
		} else {
			require.ErrorContains(t, err, "assembly inventory exceeds 4096 layers")
			require.Nil(t, result)
		}
		require.Equal(t, 1, reads, "cached empty layers count without being read again")
	}
}

func TestInventoryBudgetCountsRepeatedEntries(t *testing.T) {
	budget := inventoryBudget{entriesLeft: maxInventoryEntries, pathBytesLeft: maxInventoryPathBytes, componentsLeft: maxInventoryComponents}
	hdr := &tar.Header{Name: "same"}
	for range maxInventoryEntries {
		require.NoError(t, budget.consume(hdr))
	}
	require.ErrorContains(t, budget.consume(hdr), "250000 entries")
}

func TestInventoryBudgetCountsPathsAndLinkTargets(t *testing.T) {
	for _, name := range []string{"path", "link target"} {
		t.Run(name, func(t *testing.T) {
			budget := inventoryBudget{entriesLeft: maxInventoryEntries, pathBytesLeft: maxInventoryPathBytes, componentsLeft: maxInventoryComponents}
			hdr := &tar.Header{}
			if name == "path" {
				hdr.Name = strings.Repeat("p", 4096)
			} else {
				hdr.Name = "link"
				hdr.Linkname = strings.Repeat("t", 4092)
			}
			for range maxInventoryPathBytes / 4096 {
				require.NoError(t, budget.consume(hdr))
			}
			require.Zero(t, budget.pathBytesLeft, "the exact byte limit is accepted")
			require.ErrorContains(t, budget.consume(&tar.Header{Name: "x"}), "path and link-name bytes")
			require.ErrorContains(t, budget.consume(&tar.Header{Linkname: "x"}), "path and link-name bytes")
		})
	}
}

func TestAssembleRejectsOversizedInventoryAndClosesStream(t *testing.T) {
	input, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
	// Keep only compressed bytes in the fixture. The final filesystem has one
	// file, but the repeated archive names exceed the retained metadata budget.
	name := strings.Repeat(strings.Repeat("p", 255)+"/", 15) + strings.Repeat("p", 255)
	var blob bytes.Buffer
	gz := gzip.NewWriter(&blob)
	diffID := digest.Canonical.Digester()
	tw := tar.NewWriter(io.MultiWriter(gz, diffID.Hash()))
	for range maxInventoryPathBytes/len(name) + 1 {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0644}))
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	require.Less(t, blob.Len(), 1<<20, "a small blob can exhaust an unbounded inventory")
	layer := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageLayerGzip, Digest: digest.FromBytes(blob.Bytes()), Size: int64(blob.Len())}
	input.Manifest.Layers = []ocispec.Descriptor{layer}
	input.Config.RootFS.DiffIDs = []digest.Digest{diffID.Digest()}
	reader := &assemblyStream{Reader: bytes.NewReader(blob.Bytes())}
	input.LayerLoader = func(context.Context, ocispec.Descriptor) (io.ReadCloser, error) { return reader, nil }
	var events []Progress
	result, err := Assemble(t.Context(), fixtureRequests(1), Options{LayerValidator: ValidateLayers,
		Loader: fixtureLoader(input), OnProgress: func(p Progress) { events = append(events, p) },
	})
	require.Nil(t, result)
	require.ErrorContains(t, err, "path and link-name bytes")
	require.ErrorContains(t, err, fixtureRequests(1)[0].Reference)
	require.ErrorContains(t, err, layer.Digest.String())
	require.True(t, reader.closed)
	require.Equal(t, Progress{Stage: StageInventory, State: ProgressFailed, Reference: fixtureRequests(1)[0].Reference, Layer: layer.Digest}, events[len(events)-1])
}
