package fetch

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/docker/sandbox-kit-spec/v3/assemble"
	"github.com/docker/sandbox-kit-spec/v3/resolve"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type kitInventory struct {
	reference string
	layers    [][]tar.Header
}

// Compressed size does not bound inventory memory: repeated overwritten paths
// still need archive-order processing. Bound both fixed-size headers and the
// strings they retain before appending anything to the cacheable inventory.
const (
	maxInventoryEntries   = 250_000
	maxInventoryPathBytes = 32 << 20
)

type inventoryBudget struct {
	entriesLeft   int
	pathBytesLeft int
}

func (b *inventoryBudget) consume(hdr *tar.Header) error {
	if b.entriesLeft == 0 {
		return fmt.Errorf("layer inventory exceeds %d entries", maxInventoryEntries)
	}
	if len(hdr.Name) > b.pathBytesLeft || len(hdr.Linkname) > b.pathBytesLeft-len(hdr.Name) {
		return fmt.Errorf("layer inventory exceeds %d path and link-name bytes", maxInventoryPathBytes)
	}
	b.entriesLeft--
	b.pathBytesLeft -= len(hdr.Name) + len(hdr.Linkname)
	return nil
}

func inventoryKits(ctx context.Context, kits []*resolve.Unit, loaded map[string]*LoadedKit, report func(Progress)) ([]kitInventory, error) {
	// Descriptors are dependency-ordered; filesystem effects are workload-first.
	resolution, err := resolve.Resolve(kits)
	if err != nil {
		return nil, err
	}
	type blobKey struct {
		digest digest.Digest
		size   int64
		diffID digest.Digest
	}
	cache := map[blobKey][]tar.Header{}
	inventories := make([]kitInventory, 0, len(kits))
	for _, kit := range resolution.Ordered() {
		input := loaded[kit.Reference]
		inventory := kitInventory{reference: kit.Reference}
		for index, layer := range input.Manifest.Layers {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			diffID := input.Config.RootFS.DiffIDs[index]
			key := blobKey{layer.Digest, layer.Size, diffID}
			files, exists := cache[key]
			if !exists {
				err := progressStep(ctx, report, Progress{Stage: StageInventory, Reference: kit.Reference, Layer: layer.Digest}, func() error {
					var err error
					files, err = readLayerInventory(ctx, input, layer, diffID)
					return err
				})
				if err != nil {
					return nil, fmt.Errorf("inventory %s layer %s: %w", kit.Reference, layer.Digest, err)
				}
				cache[key] = files
			}
			inventory.layers = append(inventory.layers, files)
		}
		inventories = append(inventories, inventory)
	}
	return inventories, nil
}

func readLayerInventory(ctx context.Context, input *LoadedKit, layer ocispec.Descriptor, diffID digest.Digest) (files []tar.Header, retErr error) {
	if layer.Size == math.MaxInt64 {
		return nil, fmt.Errorf("layer size is too large")
	}
	reader, err := input.OpenLayer(ctx, layer)
	if err != nil {
		return nil, err
	}
	if reader == nil {
		return nil, fmt.Errorf("OpenLayer returned no stream")
	}
	defer func() { retErr = errors.Join(retErr, reader.Close()) }()
	// The tar reader can finish before the compressed blob does. Drain through
	// the same verifier so trailing bytes, short blobs, and truncation are judged.
	limited := &io.LimitedReader{R: contextReader{ctx: ctx, reader: reader}, N: layer.Size + 1}
	verifier := layer.Digest.Verifier()
	verified := io.TeeReader(limited, verifier)
	budget := inventoryBudget{entriesLeft: maxInventoryEntries, pathBytesLeft: maxInventoryPathBytes}
	err = assemble.WalkLayerVerified(verified, diffID, func(hdr *tar.Header) error {
		if err := budget.consume(hdr); err != nil {
			return err
		}
		// Clone names so a short substring cannot retain a larger PAX record.
		// Keep only extraction metadata, never bodies or extended attributes.
		files = append(files, tar.Header{Name: strings.Clone(hdr.Name), Typeflag: hdr.Typeflag,
			Linkname: strings.Clone(hdr.Linkname), Mode: hdr.Mode, Uid: hdr.Uid, Gid: hdr.Gid,
			Devmajor: hdr.Devmajor, Devminor: hdr.Devminor})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(io.Discard, verified); err != nil {
		return nil, err
	}
	if layer.Size+1-limited.N != layer.Size {
		return nil, fmt.Errorf("layer size mismatch")
	}
	if !verifier.Verified() {
		return nil, fmt.Errorf("layer digest mismatch")
	}
	return files, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
