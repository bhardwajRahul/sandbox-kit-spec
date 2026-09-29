package fetch

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestInventoryBudgetCountsRepeatedEntries(t *testing.T) {
	budget := inventoryBudget{entriesLeft: maxInventoryEntries, pathBytesLeft: maxInventoryPathBytes}
	hdr := &tar.Header{Name: "same"}
	for range maxInventoryEntries {
		require.NoError(t, budget.consume(hdr))
	}
	require.ErrorContains(t, budget.consume(hdr), "250000 entries")
}

func TestInventoryBudgetCountsPathsAndLinkTargets(t *testing.T) {
	for _, name := range []string{"path", "link target"} {
		t.Run(name, func(t *testing.T) {
			budget := inventoryBudget{entriesLeft: maxInventoryEntries, pathBytesLeft: maxInventoryPathBytes}
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
	name := strings.Repeat("dir/", 1023) + "file"
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
	input.OpenLayer = func(context.Context, ocispec.Descriptor) (io.ReadCloser, error) { return reader, nil }
	var events []Progress
	result, err := Assemble(t.Context(), fixtureRequests(1), Options{
		Loader: fixtureLoader(input), OnProgress: func(p Progress) { events = append(events, p) },
	})
	require.Nil(t, result)
	require.ErrorContains(t, err, "path and link-name bytes")
	require.ErrorContains(t, err, fixtureRequests(1)[0].Reference)
	require.ErrorContains(t, err, layer.Digest.String())
	require.True(t, reader.closed)
	require.Equal(t, Progress{Stage: StageInventory, State: ProgressFailed, Reference: fixtureRequests(1)[0].Reference, Layer: layer.Digest}, events[len(events)-1])
}
