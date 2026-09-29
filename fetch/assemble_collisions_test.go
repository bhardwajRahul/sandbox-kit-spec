package fetch

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func layerFixture(t *testing.T, kind, name string, layers ...[]tar.Header) *LoadedKit {
	t.Helper()
	input, _ := assemblyFixture(t, kind, name, "unused")
	input.Manifest.Layers = nil
	input.Config.RootFS.DiffIDs = nil
	blobs := map[digest.Digest][]byte{}
	for _, headers := range layers {
		var body bytes.Buffer
		tw := tar.NewWriter(&body)
		for _, header := range headers {
			require.NoError(t, tw.WriteHeader(&header))
		}
		require.NoError(t, tw.Close())
		raw := body.Bytes()
		id := digest.FromBytes(raw)
		blobs[id] = raw
		input.Manifest.Layers = append(input.Manifest.Layers, ocispec.Descriptor{MediaType: ocispec.MediaTypeImageLayer, Digest: id, Size: int64(len(raw))})
		input.Config.RootFS.DiffIDs = append(input.Config.RootFS.DiffIDs, id)
	}
	configRaw, err := json.Marshal(input.Config)
	require.NoError(t, err)
	input.Manifest.Config.Digest, input.Manifest.Config.Size = digest.FromBytes(configRaw), int64(len(configRaw))
	manifestRaw, err := json.Marshal(input.Manifest)
	require.NoError(t, err)
	input.Digest = digest.FromBytes(manifestRaw)
	input.OpenLayer = func(_ context.Context, layer ocispec.Descriptor) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(blobs[layer.Digest])), nil
	}
	return input
}

func file(name string) tar.Header { return tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0644} }
func directory(name string) tar.Header {
	return tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0755}
}
func symlink(name, target string) tar.Header {
	return tar.Header{Name: name, Typeflag: tar.TypeSymlink, Linkname: target}
}

func TestAssembleChecksFilesystemEffects(t *testing.T) {
	for _, tc := range []struct {
		name         string
		lower, upper [][]tar.Header
		collision    string
	}{
		{"whiteout file", [][]tar.Header{{file("dir/foo")}}, [][]tar.Header{{file("dir/.wh.foo")}}, "/dir/foo"},
		{"whiteout directory", [][]tar.Header{{file("dir/nested/foo")}}, [][]tar.Header{{file(".wh.dir")}}, "/dir"},
		{"opaque directory", [][]tar.Header{{file("dir/foo")}}, [][]tar.Header{{file("dir/.wh..wh..opq")}}, "/dir/foo"},
		{"cleaned alias", [][]tar.Header{{file("dir/foo")}}, [][]tar.Header{{file("/dir/bar/../foo")}}, "/dir/foo"},
		{"same-layer symlink alias", [][]tar.Header{{file("dir/foo")}}, [][]tar.Header{{symlink("alias", "dir"), file("alias/foo")}}, "/dir/foo"},
		{"directory replaces file", [][]tar.Header{{file("dir")}}, [][]tar.Header{{file("dir/foo")}}, "/dir"},
		{"file replaces directory", [][]tar.Header{{file("dir/foo")}}, [][]tar.Header{{file("dir")}}, "/dir"},
		{"lower file deleted internally", [][]tar.Header{{file("dir/foo")}, {file("dir/.wh.foo")}}, [][]tar.Header{{file("dir/foo")}}, ""},
		{"lower directory made opaque internally", [][]tar.Header{{file("dir/foo")}, {file("dir/.wh..wh..opq"), file("dir/keep")}}, [][]tar.Header{{file("dir/foo")}}, ""},
		{"lower file replaced by directory internally", [][]tar.Header{{file("dir")}, {directory("dir")}}, [][]tar.Header{{file("dir/foo")}}, ""},
		{"upper deletion survives internal layers", [][]tar.Header{{file("dir/foo")}}, [][]tar.Header{{file("dir/foo")}, {file("dir/.wh.foo")}}, "/dir/foo"},
		{"upper opaque survives internal layers", [][]tar.Header{{file("dir/foo")}}, [][]tar.Header{{file("dir/.wh..wh..opq")}, {file("dir/bar")}}, "/dir/foo"},
		{"upper directory replaced by file then directory", [][]tar.Header{{file("dir/foo")}}, [][]tar.Header{{file("dir")}, {file("dir/bar")}}, "/dir/foo"},
		{"upper deletion followed by directory", [][]tar.Header{{file("dir/foo")}}, [][]tar.Header{{file(".wh.dir")}, {file("dir/bar")}}, "/dir/foo"},
		{"opaque retains own layer files", [][]tar.Header{{file("dir/foo"), file("dir/.wh..wh..opq")}}, [][]tar.Header{{file("dir/foo")}}, "/dir/foo"},
		{"same layer whiteout retains written file", [][]tar.Header{{file("dir/foo"), file("dir/.wh.foo")}}, [][]tar.Header{{file("dir/foo")}}, "/dir/foo"},
		{"shared directories", [][]tar.Header{{directory("dir"), file("dir/base")}}, [][]tar.Header{{directory("dir"), file("dir/tool")}}, ""},
		{"whiteout absent lower path", [][]tar.Header{{file("dir/base")}}, [][]tar.Header{{file("dir/.wh.absent")}}, ""},
		{"whiteout sibling prefix", [][]tar.Header{{file("directory/base")}}, [][]tar.Header{{file(".wh.dir")}}, ""},
		{"opaque sibling prefix", [][]tar.Header{{file("directory/base")}}, [][]tar.Header{{file("dir/.wh..wh..opq")}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := layerFixture(t, spec.KindWorkload, "base", tc.lower...)
			mixin := layerFixture(t, spec.KindMixin, "tool", tc.upper...)
			result, err := Assemble(t.Context(), fixtureRequests(2), Options{Loader: fixtureLoader(base, mixin)})
			if tc.collision == "" {
				require.NoError(t, err)
				require.NotNil(t, result)
				return
			}
			require.Nil(t, result)
			require.ErrorContains(t, err, "kit file collisions")
			require.ErrorContains(t, err, tc.collision)
			require.ErrorContains(t, err, fixtureRequests(2)[0].Reference)
			require.ErrorContains(t, err, fixtureRequests(2)[1].Reference)
		})
	}
}

func TestAssembleCollisionsRetainOwnersAcrossKits(t *testing.T) {
	base := layerFixture(t, spec.KindWorkload, "base", []tar.Header{file("dir/z")})
	first := layerFixture(t, spec.KindMixin, "first", []tar.Header{file("dir/a")})
	second := layerFixture(t, spec.KindMixin, "second", []tar.Header{file("dir/.wh..wh..opq")})
	result, err := Assemble(t.Context(), fixtureRequests(3), Options{Loader: fixtureLoader(base, first, second)})
	require.Nil(t, result)
	require.ErrorContains(t, err, "/dir/z")
	require.ErrorContains(t, err, fixtureRequests(3)[0].Reference)
	require.ErrorContains(t, err, fixtureRequests(3)[2].Reference)
}

func TestCollisionsWalkDeepDirectoryPrefixes(t *testing.T) {
	name := strings.Repeat("d/", 1000) + "file"
	inventories := []kitInventory{
		{reference: "base", layers: [][]inventoryEntry{{{name: name, typeflag: tar.TypeReg}}}},
		{reference: "empty", layers: [][]inventoryEntry{nil}},
	}
	require.NoError(t, checkFileCollisions(t.Context(), inventories))
	inventories = append(inventories, kitInventory{reference: "upper", layers: [][]inventoryEntry{{{name: strings.Repeat("d/", 1000) + ".wh.file", typeflag: tar.TypeReg}}}})
	err := checkFileCollisions(t.Context(), inventories)
	require.ErrorContains(t, err, "/"+name)
	require.ErrorContains(t, err, "upper replaces or deletes a path contributed by base")
}

// Exercise the full allowed owner set followed by the maximum empty-Kit tail.
// Runtime should be dominated by building the first Kit's filesystem, not by
// repeatedly sorting its paths for every following Kit. No timing assertion:
// the benchmark makes scaling measurable without a machine-dependent test.
func BenchmarkCollisionsWithEmptyKits(b *testing.B) {
	for _, emptyKits := range []int{0, maxInventoryLayers - 1} {
		b.Run(fmt.Sprintf("empty-kits-%d", emptyKits), func(b *testing.B) {
			entries := make([]inventoryEntry, maxInventoryEntries)
			for i := range entries {
				entries[i] = inventoryEntry{name: fmt.Sprintf("file-%06d", i), typeflag: tar.TypeReg}
			}
			inventories := make([]kitInventory, emptyKits+1)
			inventories[0] = kitInventory{reference: "base", layers: [][]inventoryEntry{entries}}
			for i := 1; i < len(inventories); i++ {
				inventories[i] = kitInventory{reference: fmt.Sprintf("empty-%d", i), layers: [][]inventoryEntry{nil}}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := checkFileCollisions(b.Context(), inventories); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestAssembleFilesystemOrderIsWorkloadFirst(t *testing.T) {
	base := layerFixture(t, spec.KindWorkload, "base", []tar.Header{file("dir/foo")})
	base.Descriptor = kitJSON(t, &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload, Requires: []string{"tool"}})
	mixin := layerFixture(t, spec.KindMixin, "tool", []tar.Header{file("dir/.wh.foo")})
	// The mixin comes first in dependency order, but its whiteout is applied
	// over the workload. Checking in dependency order would miss the deletion.
	_, err := Assemble(t.Context(), fixtureRequests(2), Options{Loader: fixtureLoader(base, mixin)})
	require.ErrorContains(t, err, "kit file collisions")
	require.ErrorContains(t, err, "/dir/foo")
}

func TestAssembleRejectsUnextractableLayerEntry(t *testing.T) {
	input := layerFixture(t, spec.KindWorkload, "base", []tar.Header{file("blocked"), file("blocked/child")})
	result, err := Assemble(t.Context(), fixtureRequests(1), Options{Loader: fixtureLoader(input)})
	require.Nil(t, result)
	require.ErrorContains(t, err, "cannot be extracted")
}
